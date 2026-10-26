// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/operate"
)

func TestDeskTermEndEventCarriesSizesNotContents(t *testing.T) {
	tenant, peer, user := uuid.New(), uuid.New(), uuid.New()
	ev, err := deskTermEndEvent(
		deskTermMeta{tenantID: tenant, actorType: "user", actorID: &user, userAgent: "curl/8"},
		peer,
		operate.PTYEnd{OpID: "op-1", Reason: operate.EndExit, ExitCode: 2, Duration: 1500 * time.Millisecond, BytesIn: 12, BytesOut: 3456},
	)
	if err != nil {
		t.Fatal(err)
	}
	if ev.Action != "peer.desk.term.end" || ev.ActorType != "user" || *ev.ActorID != user || *ev.TenantID != tenant || *ev.ResourceID != peer {
		t.Fatalf("event = %+v", ev)
	}
	if ev.UserAgent == nil || *ev.UserAgent != "curl/8" {
		t.Fatalf("user agent = %v", ev.UserAgent)
	}
	var diff map[string]any
	if err := json.Unmarshal(ev.Diff, &diff); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"opId": "op-1", "reason": "exit", "exitCode": 2.0, "durationMs": 1500.0, "bytesIn": 12.0, "bytesOut": 3456.0}
	for k, v := range want {
		if diff[k] != v {
			t.Errorf("diff[%s] = %v, want %v", k, diff[k], v)
		}
	}
	if len(diff) != len(want) {
		t.Errorf("unexpected fields in diff: %s", strings.TrimSpace(string(ev.Diff)))
	}
}
