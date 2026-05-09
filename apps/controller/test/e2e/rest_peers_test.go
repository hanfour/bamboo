// SPDX-License-Identifier: AGPL-3.0-or-later

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRESTRegister_HappyPath drives /api/v1/peers/register through the
// HTTP fixture and verifies the JSON shape matches the gRPC handler's
// behavior (same code path, different transport).
func TestRESTRegister_HappyPath(t *testing.T) {
	f := startFixture(t)

	body := map[string]any{
		"hostname":           "rest-mac-1",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "darwin",
		"clientVersion":      "0.0.1",
		"tenantSlug":         f.tenantSlug,
	}
	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if resp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", resp.status, resp.body)
	}

	var got struct {
		Self struct {
			ID       string `json:"id"`
			TenantID string `json:"tenantId"`
			Hostname string `json:"hostname"`
			IP       string `json:"ip"`
		} `json:"self"`
		Peers          []map[string]any `json:"peers"`
		PolicyRevision int64            `json:"policyRevision"`
	}
	if err := json.Unmarshal(resp.body, &got); err != nil {
		t.Fatalf("decode: %v; body=%s", err, resp.body)
	}
	if got.Self.ID == "" || got.Self.IP == "" || got.Self.TenantID == "" {
		t.Errorf("missing self fields: %+v", got.Self)
	}
	if got.Self.Hostname != "rest-mac-1" {
		t.Errorf("Self.Hostname = %q, want rest-mac-1", got.Self.Hostname)
	}
	if got.PolicyRevision == 0 {
		t.Error("PolicyRevision should be >= 1 after register")
	}
}

// TestRESTRegister_RejectsMissingFields verifies validation forwards
// from the gRPC handler as a 400 Bad Request.
func TestRESTRegister_RejectsMissingFields(t *testing.T) {
	f := startFixture(t)

	resp := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname": "no-key",
	})
	if resp.status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400; body=%s", resp.status, resp.body)
	}
}

// TestRESTHeartbeat_HappyPath registers a peer over REST then sends a
// heartbeat for it. Both calls hit the same CoordinatorHandler the gRPC
// path uses, so peers_changed/policy_changed semantics carry over.
func TestRESTHeartbeat_HappyPath(t *testing.T) {
	f := startFixture(t)

	// Register first.
	body := map[string]any{
		"hostname":           "rest-mac-2",
		"wireguardPublicKey": randomPubKey(t),
		"os":                 "darwin",
		"tenantSlug":         f.tenantSlug,
	}
	regResp := postJSON(t, f.httpURL+"/api/v1/peers/register", body)
	if regResp.status != http.StatusOK {
		t.Fatalf("register status = %d, body=%s", regResp.status, regResp.body)
	}
	var reg struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(regResp.body, &reg)
	if reg.Self.ID == "" {
		t.Fatal("register did not return self.id")
	}

	// Heartbeat with a stale revision should report policyChanged=true.
	hbResp := postJSON(t, f.httpURL+"/api/v1/peers/heartbeat", map[string]any{
		"peerId":              reg.Self.ID,
		"knownPolicyRevision": int64(0),
	})
	if hbResp.status != http.StatusOK {
		t.Fatalf("heartbeat status = %d, body=%s", hbResp.status, hbResp.body)
	}
	var hb struct {
		PolicyChanged         bool  `json:"policyChanged"`
		CurrentPolicyRevision int64 `json:"currentPolicyRevision"`
	}
	if err := json.Unmarshal(hbResp.body, &hb); err != nil {
		t.Fatalf("decode hb: %v", err)
	}
	if !hb.PolicyChanged {
		t.Errorf("heartbeat with revision 0 should report policyChanged=true; got %+v", hb)
	}
	if hb.CurrentPolicyRevision == 0 {
		t.Errorf("heartbeat returned currentPolicyRevision=0")
	}
}

// TestRESTWatch_StreamsRegisteredPeer subscribes to /api/v1/peers/watch
// for an existing peer, then registers a second peer over REST and
// asserts the SSE stream emits a peer_added event for it.
func TestRESTWatch_StreamsRegisteredPeer(t *testing.T) {
	f := startFixture(t)

	// Register the watcher peer.
	first := postJSON(t, f.httpURL+"/api/v1/peers/register", map[string]any{
		"hostname":           "rest-mac-watcher",
		"wireguardPublicKey": randomPubKey(t),
		"tenantSlug":         f.tenantSlug,
	})
	if first.status != http.StatusOK {
		t.Fatalf("register watcher status=%d body=%s", first.status, first.body)
	}
	var watcher struct {
		Self struct {
			ID string `json:"id"`
		} `json:"self"`
	}
	_ = json.Unmarshal(first.body, &watcher)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		f.httpURL+"/api/v1/peers/watch?peerId="+watcher.Self.ID, nil)
	if err != nil {
		t.Fatalf("build watch request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("watch request: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("watch status=%d body=%s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}

	// Trigger a peer_added by registering a second peer. The bus
	// publishes the event after the response is built, so a small
	// delay between Register returning and the event arriving on the
	// stream is expected.
	go func() {
		_ = postJSON(nil, f.httpURL+"/api/v1/peers/register", map[string]any{
			"hostname":           "rest-mac-other",
			"wireguardPublicKey": randomPubKey(t),
			"tenantSlug":         f.tenantSlug,
		})
	}()

	got, err := readSSEEvent(resp.Body, "peer_added", 3*time.Second)
	if err != nil {
		t.Fatalf("waiting for peer_added: %v", err)
	}
	if !strings.Contains(got, "rest-mac-other") {
		t.Errorf("peer_added payload missing hostname; got %q", got)
	}
}

// jsonResponse bundles an HTTP response status + body for the small
// post-and-decode tests above.
type jsonResponse struct {
	status int
	body   []byte
}

func postJSON(t *testing.T, url string, payload any) jsonResponse {
	if t != nil {
		t.Helper()
	}
	buf, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(buf))
	if err != nil {
		if t != nil {
			t.Fatalf("POST %s: %v", url, err)
		}
		return jsonResponse{}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return jsonResponse{status: resp.StatusCode, body: body}
}

// readSSEEvent reads from r until it encounters an event of the named
// type, then returns the data line for that event. Times out after d.
func readSSEEvent(r io.Reader, name string, d time.Duration) (string, error) {
	type result struct {
		data string
		err  error
	}
	out := make(chan result, 1)
	go func() {
		buf := make([]byte, 0, 4096)
		chunk := make([]byte, 1024)
		var currentEvent string
		for {
			n, err := r.Read(chunk)
			if n > 0 {
				buf = append(buf, chunk[:n]...)
				for {
					idx := bytes.Index(buf, []byte("\n"))
					if idx < 0 {
						break
					}
					line := string(buf[:idx])
					buf = buf[idx+1:]
					switch {
					case strings.HasPrefix(line, "event: "):
						currentEvent = strings.TrimPrefix(line, "event: ")
					case strings.HasPrefix(line, "data: "):
						if currentEvent == name {
							out <- result{data: strings.TrimPrefix(line, "data: ")}
							return
						}
					}
				}
			}
			if err != nil {
				out <- result{err: err}
				return
			}
		}
	}()
	select {
	case r := <-out:
		return r.data, r.err
	case <-time.After(d):
		return "", context.DeadlineExceeded
	}
}
