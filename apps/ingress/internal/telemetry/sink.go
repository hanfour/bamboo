// SPDX-License-Identifier: AGPL-3.0-or-later

// Package telemetry ships per-request metadata to the controller, which
// stores it in ClickHouse connection_events. Bodies and headers never
// enter this package: proxy.Record has no field for them.
package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
	"github.com/hanfour/bamboo/apps/ingress/internal/routes"
)

// The controller's POST /internal/v1/ingress/events limits; a batch over
// either is rejected whole.
const (
	maxBatchEvents = 1000
	maxBatchBytes  = 1 << 20
)

// SinkOptions configure a Sink. Zero values get defaults.
type SinkOptions struct {
	ControllerURL   string
	TokenPath       string
	Client          *http.Client
	QueueSize       int
	BatchSize       int
	FlushEvery      time.Duration
	ShutdownTimeout time.Duration
	Log             *slog.Logger
	OnDrop          func(n int)
}

// Sink buffers records in a bounded queue and posts them in batches.
// Delivery is best effort: a failed post is dropped, never retried.
type Sink struct {
	o       SinkOptions
	ch      chan proxy.Record
	dropped atomic.Uint64
}

type eventJSON struct {
	ServiceID  string    `json:"service_id"`
	ClientIP   string    `json:"client_ip"`
	Status     int       `json:"status"`
	BytesIn    int64     `json:"bytes_in"`
	BytesOut   int64     `json:"bytes_out"`
	DurationMs int64     `json:"duration_ms"`
	OccurredAt time.Time `json:"occurred_at"`
}

// NewSink fills defaults: queue 4096, batch 500 (at most 1000), flush
// every 5 s, shutdown flush bounded to 5 s.
func NewSink(o SinkOptions) *Sink {
	if o.QueueSize <= 0 {
		o.QueueSize = 4096
	}
	if o.BatchSize <= 0 {
		o.BatchSize = 500
	}
	o.BatchSize = min(o.BatchSize, maxBatchEvents)
	if o.FlushEvery <= 0 {
		o.FlushEvery = 5 * time.Second
	}
	if o.ShutdownTimeout <= 0 {
		o.ShutdownTimeout = 5 * time.Second
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.ControllerURL = strings.TrimSuffix(o.ControllerURL, "/")
	return &Sink{o: o, ch: make(chan proxy.Record, o.QueueSize)}
}

// Record enqueues r. When the queue is full the oldest record is dropped
// to make room, so recent traffic wins. Never blocks.
func (s *Sink) Record(r proxy.Record) {
	for range 3 {
		select {
		case s.ch <- r:
			return
		default:
		}
		select {
		case <-s.ch:
			s.drop(1)
		default:
		}
	}
	s.drop(1) // lost the race to concurrent producers; drop r itself
}

// Dropped reports records lost to a full queue or a failed post.
func (s *Sink) Dropped() uint64 { return s.dropped.Load() }

func (s *Sink) drop(n int) {
	s.dropped.Add(uint64(n))
	if s.o.OnDrop != nil {
		s.o.OnDrop(n)
	}
}

// Run batches until ctx ends, then drains the queue within
// ShutdownTimeout and returns.
func (s *Sink) Run(ctx context.Context) {
	tick := time.NewTicker(s.o.FlushEvery)
	defer tick.Stop()
	// A post in flight when ctx ends is allowed to finish (bounded by the
	// client timeout) rather than being cancelled and lost.
	postCtx := context.WithoutCancel(ctx)
	batch := make([]proxy.Record, 0, s.o.BatchSize)
	for {
		select {
		case <-ctx.Done():
			s.shutdown(batch)
			return
		case r := <-s.ch:
			batch = append(batch, r)
			if len(batch) >= s.o.BatchSize {
				s.send(postCtx, batch)
				batch = batch[:0]
			}
		case <-tick.C:
			s.send(postCtx, batch)
			batch = batch[:0]
		}
	}
}

// shutdown posts the held batch and everything still queued. Once the
// deadline passes, the remainder is counted as dropped.
func (s *Sink) shutdown(batch []proxy.Record) {
	ctx, cancel := context.WithTimeout(context.Background(), s.o.ShutdownTimeout)
	defer cancel()
	for {
		select {
		case r := <-s.ch:
			batch = append(batch, r)
			if len(batch) < s.o.BatchSize {
				continue
			}
		default:
		}
		s.send(ctx, batch)
		if len(batch) < s.o.BatchSize {
			return // queue empty
		}
		batch = batch[:0]
	}
}

// send posts recs, splitting them until each body fits maxBatchBytes.
// Failures are counted as drops and logged without request details.
func (s *Sink) send(ctx context.Context, recs []proxy.Record) {
	if len(recs) == 0 {
		return
	}
	if ctx.Err() != nil {
		s.drop(len(recs))
		return
	}
	body, err := encode(recs)
	if err == nil && len(body) > maxBatchBytes {
		if len(recs) > 1 {
			mid := len(recs) / 2
			s.send(ctx, recs[:mid])
			s.send(ctx, recs[mid:])
			return
		}
		err = errors.New("single event exceeds the batch byte limit")
	}
	if err == nil {
		err = s.post(ctx, body)
	}
	if err != nil {
		s.drop(len(recs))
		s.o.Log.Warn("telemetry post failed; batch dropped", "n", len(recs), "err", err)
	}
}

func encode(recs []proxy.Record) ([]byte, error) {
	events := make([]eventJSON, 0, len(recs))
	for _, r := range recs {
		events = append(events, eventJSON{
			ServiceID: r.ServiceID, ClientIP: r.ClientIP, Status: r.Status, BytesIn: r.BytesIn,
			BytesOut: r.BytesOut, DurationMs: r.Duration.Milliseconds(), OccurredAt: r.At.UTC(),
		})
	}
	return json.Marshal(struct {
		Events []eventJSON `json:"events"`
	}{events})
}

// post sends one body. The token is re-read every time so a rotated
// token is picked up without a restart; it never appears in an error.
func (s *Sink) post(ctx context.Context, body []byte) error {
	tok, err := routes.ReadToken(s.o.TokenPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.o.ControllerURL+"/internal/v1/ingress/events", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("post events: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("controller responded %d", resp.StatusCode)
	}
	return nil
}
