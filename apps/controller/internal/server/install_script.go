// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	_ "embed"
	"net/http"
	"net/url"
	"strings"
)

//go:embed install.sh
var installScript string

// installScriptFor returns the installer with the controller's public URL
// filled in as the default server. The URL goes into a shell string, so it
// must be a plain http(s) origin: anything else (quotes, spaces, `$`) is
// refused rather than escaped.
func installScriptFor(baseURL string) (string, bool) {
	u, err := url.Parse(baseURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	origin := u.Scheme + "://" + u.Host
	if strings.ContainsAny(origin, "\x00 \t\r\n\"'`$\\;&|<>(){}*?[]!#~") {
		return "", false
	}
	return strings.ReplaceAll(installScript, "__BAMBOO_SERVER__", origin), true
}

// handleInstallScript serves GET /install.sh. It is public on purpose: the
// script holds no secret, only the controller's own URL.
func (h *HTTPServer) handleInstallScript(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	script, ok := installScriptFor(h.baseURL)
	if !ok {
		http.Error(w, "install script unavailable: the controller has no valid public base_url", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(script))
}
