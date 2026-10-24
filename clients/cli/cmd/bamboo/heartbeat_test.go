// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
)

// TestRESTHeartbeater_WireShapeAndBearer pins the §4 P2 §187 contract
// between the CLI and controller's REST heartbeat endpoint:
//   - bytesSent / bytesReceived appear in the JSON body so the
//     controller's bandwidth-sample side-channel can write a row.
//   - The peer-session bearer (when set) lands as
//     Authorization: Bearer <tok>, matching the controller's prod-mode
//     gate for peer-id-scoped endpoints.
//   - X-Tenant-Slug rides along for dev controllers without the gate.
//   - The 200 response decodes into HeartbeatResult fields.
//
// Without this, a refactor that drops the bytes from the JSON body
// (e.g. a struct rename without updating tags) would silently disable
// the bandwidth chart for every CLI user.
func TestRESTHeartbeater_WireShapeAndBearer(t *testing.T) {
	var (
		gotPath string
		gotAuth string
		gotSlug string
		gotBody restHeartbeatRequest
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotSlug = r.Header.Get("X-Tenant-Slug")
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &gotBody)
		_, _ = w.Write([]byte(`{"peersChanged":false,"policyChanged":true,"currentPolicyRevision":42}`))
	}))
	defer srv.Close()

	sess := &peerSession{}
	sess.set("tok-xyz", time.Now().Add(time.Hour))
	hb := &restHeartbeater{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
		session:    sess,
		tenantSlug: "acme",
	}

	res, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{
		PeerID:              "peer-1",
		KnownPolicyRevision: 7,
		Endpoints:           []string{"203.0.113.5:51820"},
		BytesSent:           12345,
		BytesReceived:       67890,
	})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}

	if gotPath != "/api/v1/peers/heartbeat" {
		t.Errorf("path = %q, want /api/v1/peers/heartbeat", gotPath)
	}
	if gotAuth != "Bearer tok-xyz" {
		t.Errorf("Authorization = %q, want Bearer tok-xyz", gotAuth)
	}
	if gotSlug != "acme" {
		t.Errorf("X-Tenant-Slug = %q, want acme", gotSlug)
	}
	if gotBody.PeerID != "peer-1" || gotBody.KnownPolicyRevision != 7 {
		t.Errorf("body id/rev = (%q, %d), want (peer-1, 7)", gotBody.PeerID, gotBody.KnownPolicyRevision)
	}
	if gotBody.BytesSent != 12345 || gotBody.BytesReceived != 67890 {
		t.Errorf("body bytes = (%d, %d), want (12345, 67890); a zero here disables the chart silently",
			gotBody.BytesSent, gotBody.BytesReceived)
	}
	if !res.PolicyChanged || res.CurrentPolicyRevision != 42 {
		t.Errorf("result = %+v, want PolicyChanged=true Rev=42", res)
	}
}

// TestRESTHeartbeater_OmitsBearerAndSlugWhenEmpty locks the dev-mode
// fallback: an empty session token + slug must not produce stray
// headers, since the controller's prod-mode gate treats "header
// present, empty value" as a credentialed call that then fails
// validation. Better to send nothing than to send an empty Bearer.
func TestRESTHeartbeater_OmitsBearerAndSlugWhenEmpty(t *testing.T) {
	var gotAuth, gotSlug string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotSlug = r.Header.Get("X-Tenant-Slug")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	hb := &restHeartbeater{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
		session:    &peerSession{}, // empty token
		tenantSlug: "",
	}
	if _, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{PeerID: "peer-1"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if gotAuth != "" {
		t.Errorf("Authorization = %q, want empty (empty session must not produce a header)", gotAuth)
	}
	if gotSlug != "" {
		t.Errorf("X-Tenant-Slug = %q, want empty", gotSlug)
	}
}

// TestRESTHeartbeater_PropagatesHTTPError ensures a non-200 from the
// controller surfaces as an error (not a silent success), so the
// daemon's slog.Warn fires and the failure is observable.
func TestRESTHeartbeater_PropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	hb := &restHeartbeater{
		httpClient: srv.Client(),
		baseURL:    srv.URL,
		session:    &peerSession{},
	}
	_, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{PeerID: "peer-1"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("err = %v, want one containing '401'", err)
	}
}

func TestRESTHeartbeater_StoresRenewedPeerSession(t *testing.T) {
	exp := time.Now().Add(24 * time.Hour).Unix()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"peersChanged":false,"policyChanged":false,"currentPolicyRevision":1,"peerSessionToken":"tok-new","peerSessionExpiresAt":` + strconv.FormatInt(exp, 10) + `}`))
	}))
	defer srv.Close()

	sess := &peerSession{}
	sess.set("tok-old", time.Now().Add(time.Hour))
	hb := &restHeartbeater{httpClient: srv.Client(), baseURL: srv.URL, session: sess}
	if _, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{PeerID: "peer-1"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if sess.Token() != "tok-new" {
		t.Errorf("token = %q, want tok-new", sess.Token())
	}
	if !sess.live(time.Now()) {
		t.Fatal("renewed token is not live")
	}
}

func TestRESTHeartbeater_LeavesTokenWhenRenewalAbsent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"peersChanged":false,"policyChanged":false,"currentPolicyRevision":1}`))
	}))
	defer srv.Close()

	sess := &peerSession{}
	sess.set("tok-old", time.Now().Add(time.Hour))
	hb := &restHeartbeater{httpClient: srv.Client(), baseURL: srv.URL, session: sess}
	if _, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{PeerID: "peer-1"}); err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if sess.Token() != "tok-old" {
		t.Errorf("token = %q, want tok-old", sess.Token())
	}
}

func TestRefreshingHeartbeater_RetriesAfterRefresh(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		if calls == 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"currentPolicyRevision":3}`))
	}))
	defer srv.Close()

	var refreshes int
	inner := &restHeartbeater{httpClient: srv.Client(), baseURL: srv.URL, session: &peerSession{}}
	hb := newRefreshingHeartbeater(inner, func(context.Context) error {
		refreshes++
		return nil
	})
	res, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{PeerID: "peer-1"})
	if err != nil {
		t.Fatalf("Heartbeat: %v", err)
	}
	if refreshes != 1 || calls != 2 {
		t.Errorf("refreshes=%d calls=%d, want 1 and 2", refreshes, calls)
	}
	if res.CurrentPolicyRevision != 3 {
		t.Errorf("revision = %d, want 3", res.CurrentPolicyRevision)
	}
}

func TestRefreshingHeartbeater_KeepsErrorWhenRefreshFails(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls++
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	inner := &restHeartbeater{httpClient: srv.Client(), baseURL: srv.URL, session: &peerSession{}}
	hb := newRefreshingHeartbeater(inner, func(context.Context) error {
		return errors.New("refresh failed")
	})
	if _, err := hb.Heartbeat(context.Background(), clientsync.HeartbeatArgs{PeerID: "peer-1"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401", err)
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1 (failed refresh must not retry the heartbeat)", calls)
	}
}
