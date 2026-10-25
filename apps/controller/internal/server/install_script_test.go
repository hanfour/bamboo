// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestInstallScriptFor(t *testing.T) {
	got, ok := installScriptFor("https://bamboo.example.com/")
	if !ok {
		t.Fatal("a plain https origin must be accepted")
	}
	if !strings.Contains(got, `SERVER="${BAMBOO_SERVER:-https://bamboo.example.com}"`) {
		t.Error("server URL not substituted")
	}
	if strings.Contains(got, "__BAMBOO_SERVER__") {
		t.Error("placeholder left behind")
	}
	for _, bad := range []string{
		"", "ftp://x", "https://", "https://u:p@x", "https://x/path", "https://x?a=1",
		`https://x";rm -rf /;"`, "https://x$(id)", "https://x`id`",
	} {
		if _, ok := installScriptFor(bad); ok {
			t.Errorf("installScriptFor(%q) must be refused", bad)
		}
	}
}

func TestInstallScriptIsValidShell(t *testing.T) {
	script, _ := installScriptFor("https://bamboo.example.com")
	f := t.TempDir() + "/install.sh"
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v\n%s", err, out)
	}
}

func TestHandleInstallScript(t *testing.T) {
	h := &HTTPServer{baseURL: "https://bamboo.example.com"}
	rr := httptest.NewRecorder()
	h.handleInstallScript(rr, httptest.NewRequest(http.MethodGet, "/install.sh", nil))
	if rr.Code != http.StatusOK || !strings.HasPrefix(rr.Body.String(), "#!/bin/sh") {
		t.Fatalf("status %d, body starts %q", rr.Code, rr.Body.String()[:min(20, rr.Body.Len())])
	}
	if ct := rr.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/x-shellscript") {
		t.Errorf("content-type = %q", ct)
	}

	rr = httptest.NewRecorder()
	h.handleInstallScript(rr, httptest.NewRequest(http.MethodPost, "/install.sh", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rr.Code)
	}

	rr = httptest.NewRecorder()
	(&HTTPServer{baseURL: ""}).handleInstallScript(rr, httptest.NewRequest(http.MethodGet, "/install.sh", nil))
	if rr.Code != http.StatusServiceUnavailable {
		t.Errorf("no base URL = %d, want 503", rr.Code)
	}
}
