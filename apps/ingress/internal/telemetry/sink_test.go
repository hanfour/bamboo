// SPDX-License-Identifier: AGPL-3.0-or-later

package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hanfour/bamboo/apps/ingress/internal/proxy"
)

func tokenFile(t *testing.T) string {
	p := filepath.Join(t.TempDir(), "tok")
	_ = os.WriteFile(p, []byte("tok-1"), 0o600)
	return p
}

func TestSink_BatchesToController(t *testing.T) {
	got := make(chan []byte, 4)
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/ingress/events" || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		auth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		got <- b
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	s := NewSink(SinkOptions{ControllerURL: srv.URL, TokenPath: tokenFile(t), FlushEvery: 20 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go s.Run(ctx)
	for i := 0; i < 3; i++ {
		s.Record(proxy.Record{ServiceID: "svc-1", ClientIP: "203.0.113.7", Status: 200, BytesIn: 1, BytesOut: 2, Duration: 1500 * time.Millisecond, At: time.Now()})
	}
	var body []byte
	select {
	case body = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("no batch posted")
	}
	if auth != "Bearer tok-1" {
		t.Errorf("Authorization = %q", auth)
	}
	var payload struct {
		Events []map[string]any `json:"events"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Events) != 3 {
		t.Fatalf("payload = %s (%v)", body, err)
	}
	e := payload.Events[0]
	if e["service_id"] != "svc-1" || e["status"].(float64) != 200 || e["duration_ms"].(float64) != 1500 {
		t.Errorf("event = %v", e)
	}
	for k := range e {
		if strings.Contains(strings.ToLower(k), "body") || strings.Contains(strings.ToLower(k), "authorization") {
			t.Errorf("telemetry carries %q", k)
		}
	}
}

func TestSink_DropsWhenFull(t *testing.T) {
	s := NewSink(SinkOptions{ControllerURL: "http://127.0.0.1:1", TokenPath: tokenFile(t), QueueSize: 1})
	for i := 0; i < 5; i++ {
		s.Record(proxy.Record{ServiceID: "svc-1", Status: 200})
	}
	if s.Dropped() != 4 {
		t.Errorf("Dropped = %d, want 4 (Record must never block a request)", s.Dropped())
	}
}

func TestSink_DropsOldestAndReportsDrops(t *testing.T) {
	var reported int
	s := NewSink(SinkOptions{ControllerURL: "http://127.0.0.1:1", TokenPath: tokenFile(t), QueueSize: 2,
		OnDrop: func(n int) { reported += n }})
	for _, id := range []string{"a", "b", "c"} {
		s.Record(proxy.Record{ServiceID: id, Status: 200})
	}
	if s.Dropped() != 1 || reported != 1 {
		t.Fatalf("Dropped = %d, OnDrop total = %d, want 1/1", s.Dropped(), reported)
	}
	first, second := <-s.ch, <-s.ch
	if first.ServiceID != "b" || second.ServiceID != "c" {
		t.Errorf("queue = %s,%s, want b,c (oldest dropped)", first.ServiceID, second.ServiceID)
	}
}

// collector is a controller stub that records each posted batch.
type collector struct {
	mu      sync.Mutex
	batches [][]eventJSON
	sizes   []int
	auths   []string
	status  int
}

func (c *collector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	var p struct {
		Events []eventJSON `json:"events"`
	}
	_ = json.Unmarshal(b, &p)
	c.mu.Lock()
	c.batches = append(c.batches, p.Events)
	c.sizes = append(c.sizes, len(b))
	c.auths = append(c.auths, r.Header.Get("Authorization"))
	status := c.status
	c.mu.Unlock()
	if status == 0 {
		status = http.StatusAccepted
	}
	w.WriteHeader(status)
}

func (c *collector) total() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, b := range c.batches {
		n += len(b)
	}
	return n
}

func TestSink_RespectsControllerBatchLimits(t *testing.T) {
	c := &collector{}
	srv := httptest.NewServer(c)
	defer srv.Close()
	s := NewSink(SinkOptions{ControllerURL: srv.URL, TokenPath: tokenFile(t), QueueSize: 5000, BatchSize: 1000, FlushEvery: time.Hour})
	// 1500 events of ~2 KiB each: the count cap splits at 1000 and the
	// byte cap splits each of those again.
	big := strings.Repeat("x", 2048)
	for i := 0; i < 1500; i++ {
		s.Record(proxy.Record{ServiceID: "svc-1", ClientIP: big, Status: 200, At: time.Now()})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel() // shutdown flush drains the queue
	<-done
	if c.total() != 1500 || s.Dropped() != 0 {
		t.Fatalf("delivered %d, dropped %d, want 1500/0", c.total(), s.Dropped())
	}
	for i, b := range c.batches {
		if len(b) > maxBatchEvents || c.sizes[i] > maxBatchBytes {
			t.Errorf("batch %d: %d events, %d bytes", i, len(b), c.sizes[i])
		}
	}
}

func TestSink_BatchSizeClampedToControllerLimit(t *testing.T) {
	if s := NewSink(SinkOptions{BatchSize: 5000}); s.o.BatchSize > maxBatchEvents {
		t.Errorf("BatchSize = %d, want <= %d", s.o.BatchSize, maxBatchEvents)
	}
}

func TestSink_ShutdownFlushIsBounded(t *testing.T) {
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-hang:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(hang)
	s := NewSink(SinkOptions{ControllerURL: srv.URL, TokenPath: tokenFile(t), FlushEvery: time.Hour, ShutdownTimeout: 100 * time.Millisecond})
	s.Record(proxy.Record{ServiceID: "svc-1", Status: 200})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not return after the shutdown timeout")
	}
	if s.Dropped() != 1 {
		t.Errorf("Dropped = %d, want 1", s.Dropped())
	}
}

func TestSink_RereadsRotatedTokenAndNeverLogsIt(t *testing.T) {
	c := &collector{status: http.StatusInternalServerError}
	srv := httptest.NewServer(c)
	defer srv.Close()
	path := tokenFile(t)
	var logs bytes.Buffer
	var logMu sync.Mutex
	log := slog.New(slog.NewTextHandler(&lockedWriter{w: &logs, mu: &logMu}, nil))
	s := NewSink(SinkOptions{ControllerURL: srv.URL, TokenPath: path, FlushEvery: time.Hour, Log: log})

	s.send(context.Background(), []proxy.Record{{ServiceID: "svc-1", Status: 200}})
	_ = os.WriteFile(path, []byte("tok-2\n"), 0o600)
	s.send(context.Background(), []proxy.Record{{ServiceID: "svc-1", Status: 200}})

	if len(c.auths) != 2 || c.auths[0] != "Bearer tok-1" || c.auths[1] != "Bearer tok-2" {
		t.Errorf("Authorization headers = %q", c.auths)
	}
	if s.Dropped() != 2 {
		t.Errorf("Dropped = %d, want 2 (controller answered 500)", s.Dropped())
	}
	logMu.Lock()
	out := logs.String()
	logMu.Unlock()
	if out == "" || strings.Contains(out, "tok-") || strings.Contains(out, "Bearer") {
		t.Errorf("log output = %q (want a warning without the token)", out)
	}
}

type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}
