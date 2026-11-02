// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hanfour/bamboo/apps/ingress/internal/metrics"
)

func TestAdminMux(t *testing.T) {
	srv := httptest.NewServer(adminMux(metrics.New("v-test")))
	defer srv.Close()
	for path, want := range map[string]string{
		"/healthz": "ok",
		"/version": `"service":"bamboo-ingress"`,
		"/metrics": `bamboo_ingress_build_info{version="v-test"} 1`,
	} {
		resp, err := srv.Client().Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 || !strings.Contains(string(body), want) {
			t.Errorf("%s: %d %q, want %q", path, resp.StatusCode, body, want)
		}
	}
}
