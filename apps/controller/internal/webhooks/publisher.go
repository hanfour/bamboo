// SPDX-License-Identifier: AGPL-3.0-or-later

// Package webhooks delivers tenant-scoped audit events to admin-
// configured outbound HTTP endpoints (§4 P2 commercial / SaaS
// integration). The data path:
//
//	audit.Insert(event)          // existing repo write
//	    └─► Publisher.Hook(event) // implements repo.AuditHook
//	         └─► Publisher.queue  // in-memory channel
//	              └─► worker goroutine
//	                   └─► query webhook_subscriptions for tenant
//	                        └─► POST per subscription, HMAC-signed
//
// Failure semantics for v1:
//   - At-most-once delivery. We do not persist undelivered events
//     to a DB queue; a controller crash between Hook() and the
//     POST drops the event. Acceptable for v1 because every
//     webhook event is also durably recorded in the audit_log
//     table, so the operator can reconstruct missed events from
//     there. v2 may add a deliveries table with retry semantics.
//   - Backpressure: when the in-memory channel is full, Hook
//     drops the event with a warning log rather than blocking
//     the audit-log writer. The audit row is the source of truth;
//     a missed webhook is recoverable, a blocked audit write is
//     not.
//   - Per-attempt budget: 5s HTTP timeout. Three attempts with
//     exponential backoff (1s, 5s, 30s). On final failure the
//     event is dropped with a warning log carrying the
//     subscription ID + last status code for debugging.
//
// HMAC contract:
//
//	X-Bamboo-Signature: sha256=<hex>
//
// where <hex> = HMAC-SHA256(subscription.secret, raw-request-body).
// Receivers verify by computing the same value over the raw body
// and constant-time comparing. The signature header is stable
// across retries (same body → same sig) so receivers can use it
// as an idempotency key.
package webhooks

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
)

// Event is the JSON payload shape on the wire. Kept compact +
// versioned so v2 can add fields without breaking existing
// receivers — they just see the extra keys.
//
// id is a fresh UUID per delivery batch (one Hook call → one
// Event). type is the audit Action string (e.g. "peer.register").
// data is the audit row's Diff field, passed through verbatim,
// because the audit log is already the canonical event description.
type Event struct {
	ID         string          `json:"id"`
	Type       string          `json:"type"`
	TenantID   string          `json:"tenantId"`
	OccurredAt time.Time       `json:"occurredAt"`
	ResourceID string          `json:"resourceId,omitempty"`
	ActorID    string          `json:"actorId,omitempty"`
	ActorType  string          `json:"actorType,omitempty"`
	Data       json.RawMessage `json:"data,omitempty"`
}

// SignatureHeader is the HTTP header name carrying the HMAC.
const SignatureHeader = "X-Bamboo-Signature"

// EventIDHeader carries the event UUID so a receiver can log/dedupe
// without parsing the body.
const EventIDHeader = "X-Bamboo-Event-Id"

// EventTypeHeader carries the event type so a receiver can route
// without parsing the body.
const EventTypeHeader = "X-Bamboo-Event-Type"

// queueCapacity caps the in-memory buffer between Hook and the
// worker. Tuned for "audit-log rate is well under 10/sec in
// typical use": 256 leaves plenty of headroom for a burst while
// staying small enough that a full queue surfaces fast.
const queueCapacity = 256

// Publisher orchestrates audit-event delivery to outbound webhook
// endpoints. Construct via New, register with the audit repo via
// repo.AuditLogs.WithHook, and call Run in a goroutine for the
// process lifetime.
type Publisher struct {
	repo   *repo.Webhooks
	client *http.Client
	queue  chan repo.AuditEvent

	// dropMu guards the dropped count + the once-per-batch log of
	// "queue full". Without this the publisher can flood the logs
	// with one line per dropped event during a burst.
	dropMu       sync.Mutex
	droppedSince time.Time
	droppedCount int
}

// New constructs a Publisher. httpClient may be nil; New supplies
// a default with a 5s per-request timeout. Tests inject their own
// httptest-backed transport.
func New(webhooks *repo.Webhooks, httpClient *http.Client) *Publisher {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 5 * time.Second}
	}
	return &Publisher{
		repo:         webhooks,
		client:       httpClient,
		queue:        make(chan repo.AuditEvent, queueCapacity),
		droppedSince: time.Now(),
	}
}

// Hook is the entry point called by the audit repo after a
// successful Insert. Pushes the event onto the queue; drops + logs
// when the queue is full so the audit-log writer never blocks on
// the webhook channel.
func (p *Publisher) Hook(ctx context.Context, ev *repo.AuditEvent) {
	if p == nil || ev == nil || ev.TenantID == nil {
		return
	}
	// Audit rows without a tenant scope (instance-wide events) are
	// not delivered to webhooks today — the subscription model is
	// tenant-bound. Drop silently rather than fanning out to every
	// tenant in the instance.
	copy := *ev
	select {
	case p.queue <- copy:
	default:
		p.recordDrop(copy)
	}
}

// recordDrop bumps the dropped counter and logs once per minute
// rather than per event so a sustained backpressure event doesn't
// flood the logs.
func (p *Publisher) recordDrop(ev repo.AuditEvent) {
	p.dropMu.Lock()
	defer p.dropMu.Unlock()
	p.droppedCount++
	if time.Since(p.droppedSince) > time.Minute {
		slog.Warn("webhook queue full; dropped events",
			"dropped", p.droppedCount,
			"since", p.droppedSince,
			"sample_action", ev.Action,
		)
		p.droppedCount = 0
		p.droppedSince = time.Now()
	}
}

// Run drains the queue and delivers each event to its matching
// subscriptions. Blocks until ctx is canceled. Safe to call from a
// goroutine; spawning multiple goroutines on the same Publisher
// is supported (the channel + per-event DB query bound concurrency
// naturally).
func (p *Publisher) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-p.queue:
			p.deliver(ctx, ev)
		}
	}
}

// deliver fans out one audit event to every matching active
// subscription. Failures on one subscription do NOT block the
// others — each gets its own goroutine-equivalent retry budget.
func (p *Publisher) deliver(ctx context.Context, ev repo.AuditEvent) {
	if ev.TenantID == nil {
		return
	}
	subs, err := p.repo.ActiveForEvent(ctx, *ev.TenantID, ev.Action)
	if err != nil {
		slog.Warn("webhook: list subscriptions failed",
			"tenant_id", ev.TenantID, "action", ev.Action, "err", err)
		return
	}
	if len(subs) == 0 {
		return
	}
	payload := Event{
		ID:         uuid.NewString(),
		Type:       ev.Action,
		TenantID:   ev.TenantID.String(),
		OccurredAt: ev.OccurredAt,
		Data:       ev.Diff,
	}
	if ev.ResourceID != nil {
		payload.ResourceID = ev.ResourceID.String()
	}
	if ev.ActorID != nil {
		payload.ActorID = ev.ActorID.String()
	}
	payload.ActorType = ev.ActorType
	body, err := json.Marshal(payload)
	if err != nil {
		slog.Warn("webhook: marshal payload", "err", err)
		return
	}
	for _, sub := range subs {
		p.deliverOne(ctx, sub, payload.ID, payload.Type, body)
	}
}

// retryDelays defines the exponential-backoff schedule. Length =
// number of attempts. First entry is the delay BEFORE the second
// attempt (the first attempt has no delay). Values tuned to be
// short enough to recover from a transient receiver hiccup,
// long enough to back off from a stuck receiver.
var retryDelays = []time.Duration{
	1 * time.Second,
	5 * time.Second,
	30 * time.Second,
}

// deliverOne POSTs the signed payload to one subscription, with
// retry. Returns nothing — failures are logged.
func (p *Publisher) deliverOne(ctx context.Context, sub *repo.WebhookSubscription, eventID, eventType string, body []byte) {
	sig := signature(sub.Secret, body)
	var lastStatus int
	var lastErr error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(retryDelays[attempt-1]):
			}
		}
		status, err := p.post(ctx, sub.URL, sig, eventID, eventType, body)
		lastStatus = status
		lastErr = err
		// 2xx is success. 4xx is a permanent client-side failure
		// (bad URL, auth required, payload too large) — retrying
		// won't help, so give up immediately and log loudly. 5xx +
		// transport errors are transient — retry per the backoff.
		switch {
		case err == nil && status >= 200 && status < 300:
			return
		case err == nil && status >= 400 && status < 500:
			slog.Warn("webhook: delivery rejected by receiver",
				"subscription_id", sub.ID,
				"url", sub.URL,
				"status", status,
				"event_type", eventType,
			)
			return
		}
	}
	slog.Warn("webhook: delivery exhausted retries",
		"subscription_id", sub.ID,
		"url", sub.URL,
		"last_status", lastStatus,
		"last_err", lastErr,
		"event_type", eventType,
	)
}

// post is one POST attempt. Returns (status, transport-error).
// Status is 0 when err is non-nil (transport failure before any
// HTTP exchange completed).
func (p *Publisher) post(ctx context.Context, url, sig, eventID, eventType string, body []byte) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "bamboo-controller-webhooks/1")
	req.Header.Set(SignatureHeader, "sha256="+sig)
	req.Header.Set(EventIDHeader, eventID)
	req.Header.Set(EventTypeHeader, eventType)
	resp, err := p.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	// Drain a bounded chunk of the response so the connection
	// can be reused even when the receiver writes a body. 4 KiB is
	// enough for an error message; we don't surface the body to
	// the operator yet (v2 may, via the delivery_attempts log).
	_, _ = io.CopyN(io.Discard, resp.Body, 4096)
	return resp.StatusCode, nil
}

// TestDeliver fires ONE synthetic POST to the given subscription
// and returns the receiver's HTTP status (or transport error).
// Synchronous + retry-free by design — the admin clicking "Test
// delivery" in the UI wants immediate yes/no feedback, not a
// fire-and-forget background attempt. The audit log isn't touched
// because Test is a probe, not a state-changing action.
//
// The body shape matches a real delivery so a receiver that
// already accepts production webhooks accepts the test too:
//
//	{ "id": "<uuid>", "type": "webhook.test", "tenantId": "<uuid>",
//	  "occurredAt": "<rfc3339>", "data": null }
//
// HMAC signature + event-type / event-id headers are computed the
// same way as production deliveries, so the receiver's
// verification code path is exercised.
func (p *Publisher) TestDeliver(ctx context.Context, sub *repo.WebhookSubscription) (int, error) {
	if sub == nil {
		return 0, fmt.Errorf("nil subscription")
	}
	payload := Event{
		ID:         uuid.NewString(),
		Type:       "webhook.test",
		TenantID:   sub.TenantID.String(),
		OccurredAt: time.Now().UTC(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("marshal test payload: %w", err)
	}
	sig := signature(sub.Secret, body)
	return p.post(ctx, sub.URL, sig, payload.ID, payload.Type, body)
}

// Signature is the public helper that computes the HMAC-SHA256
// hex string a receiver would verify against. Exposed so tests
// (and ops tooling) can re-derive the same value.
func Signature(secret string, body []byte) string {
	return signature(secret, body)
}

func signature(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}
