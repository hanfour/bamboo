// SPDX-License-Identifier: AGPL-3.0-or-later

package routes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// DefaultInterval is the spec's 10 s poll.
const DefaultInterval = 10 * time.Second

const maxRoutesBody = 4 << 20

// PollerOptions configures a Poller. Client, Log, Interval and OnResult
// are optional.
type PollerOptions struct {
	ControllerURL string
	TokenPath     string
	Table         *Table
	Interval      time.Duration
	Client        *http.Client
	Log           *slog.Logger
	OnResult      func(n int, err error)
}

// Poller refreshes a Table from the controller. On network errors, 5xx,
// and bad payloads the table is left as it was (spec: keep the last good
// table). A 401 or 403 means the controller rejected this ingress's
// token (rotated, expired, peer no longer eligible), so the table is
// cleared instead: a revoked ingress must stop serving.
type Poller struct {
	o    PollerOptions
	mu   sync.Mutex // serialises refreshes and guards etag
	etag string
}

// NewPoller fills defaults.
func NewPoller(o PollerOptions) *Poller {
	if o.Interval <= 0 {
		o.Interval = DefaultInterval
	}
	if o.Client == nil {
		o.Client = &http.Client{Timeout: 5 * time.Second}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	o.ControllerURL = strings.TrimSuffix(o.ControllerURL, "/")
	return &Poller{o: o}
}

// ReadToken reads the service token file. It is re-read on every call so
// a rotated token is picked up without a restart. Errors never include
// the token itself.
func ReadToken(path string) (string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // operator-configured path
	if err != nil {
		return "", fmt.Errorf("read token: %w", err)
	}
	tok := strings.TrimSpace(string(raw))
	if tok == "" {
		return "", errors.New("token file is empty")
	}
	return tok, nil
}

// Refresh fetches the routes once.
func (p *Poller) Refresh(ctx context.Context) error {
	p.mu.Lock()
	err := p.refresh(ctx)
	p.mu.Unlock()
	if p.o.OnResult != nil {
		p.o.OnResult(p.o.Table.Len(), err)
	}
	return err
}

func (p *Poller) refresh(ctx context.Context) error {
	tok, err := ReadToken(p.o.TokenPath)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.o.ControllerURL+"/internal/v1/ingress/routes", nil)
	if err != nil {
		return fmt.Errorf("routes: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if p.etag != "" {
		req.Header.Set("If-None-Match", p.etag)
	}
	resp, err := p.o.Client.Do(req)
	if err != nil {
		return fmt.Errorf("routes: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusNotModified:
		return nil
	case http.StatusOK:
	case http.StatusUnauthorized, http.StatusForbidden:
		p.o.Table.Replace([]Route{})
		p.etag = ""
		return fmt.Errorf("routes: controller rejected the token (%d); routes cleared", resp.StatusCode)
	default:
		return fmt.Errorf("routes: controller responded %d", resp.StatusCode)
	}
	rs, err := decodeRoutes(io.LimitReader(resp.Body, maxRoutesBody))
	if err != nil {
		return err
	}
	p.o.Table.Replace(rs)
	p.etag = resp.Header.Get("ETag")
	return nil
}

// decodeRoutes parses and fully validates a payload. Anything short of a
// clean JSON array of valid, unique routes is an error, so a bad response
// can never shrink or corrupt the table.
func decodeRoutes(r io.Reader) ([]Route, error) {
	dec := json.NewDecoder(r)
	var rs []Route
	if err := dec.Decode(&rs); err != nil {
		return nil, fmt.Errorf("routes: decode: %w", err)
	}
	if rs == nil {
		return nil, errors.New("routes: payload is null, expected an array")
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("routes: trailing data after payload")
	}
	if err := ValidateRoutes(rs); err != nil {
		return nil, fmt.Errorf("routes: invalid payload: %w", err)
	}
	return rs, nil
}

// Run refreshes immediately and then every Interval until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	tick := time.NewTicker(p.o.Interval)
	defer tick.Stop()
	for {
		if err := p.Refresh(ctx); err != nil && ctx.Err() == nil {
			p.o.Log.Warn("routes refresh failed", "err", err, "routes", p.o.Table.Len())
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
