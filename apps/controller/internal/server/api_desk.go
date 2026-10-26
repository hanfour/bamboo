// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/handlers"
	"github.com/hanfour/bamboo/apps/controller/internal/operate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// deskMaxGRPC is the Operate message cap. A session file is 4MiB and
// the protobuf wrapper needs room past gRPC's default 4MiB.
const deskMaxGRPC = 8 << 20

const (
	deskMaxFile = 4 << 20
	deskMaxText = 4000
	deskCallMax = 30 * time.Second
)

func (h *HTTPServer) apiDeskStatus(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.status") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	online, exec, err := h.coord.DeskStatus(ctx, peerID)
	if err != nil {
		writeDeskErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"online": online, "execEnabled": exec})
}

func (h *HTTPServer) apiDeskMsg(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.msg") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if !decodeDeskJSON(w, r, 8<<10, &body) {
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" || len(text) > deskMaxText {
		writeError(w, http.StatusBadRequest, errors.New("text must be 1 to 4000 characters"))
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	res, err := h.coord.DeskCall(ctx, peerID, operate.Frame{
		Kind: operate.KindMsg,
		From: h.deskActor(r, authn, tenant),
		Text: text,
	})
	h.finishDesk(w, r, authn, tenant, peerID, "peer.desk.msg", map[string]any{"chars": len(text)}, res, err)
}

func (h *HTTPServer) apiDeskPut(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.put") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, deskMaxFile+1)
	data, err := io.ReadAll(r.Body)
	if err != nil || len(data) > deskMaxFile {
		writeError(w, http.StatusBadRequest, errors.New("file is larger than 4MiB or unreadable"))
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	res, callErr := h.coord.DeskCall(ctx, peerID, operate.Frame{
		Kind: operate.KindPut,
		Path: path,
		Data: data,
	})
	h.finishDesk(w, r, authn, tenant, peerID, "peer.desk.put", map[string]any{"path": path, "bytes": len(data)}, res, callErr)
}

func (h *HTTPServer) apiDeskGet(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.get") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	path := r.URL.Query().Get("path")
	if strings.TrimSpace(path) == "" {
		writeError(w, http.StatusBadRequest, errors.New("path is required"))
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	res, err := h.coord.DeskCall(ctx, peerID, operate.Frame{Kind: operate.KindGet, Path: path})
	if err != nil {
		if !deskLookupErr(err) {
			h.auditDesk(r, authn, tenant, peerID, "peer.desk.get", map[string]any{"path": path, "ok": false, "error": err.Error()})
		}
		writeDeskErr(w, err)
		return
	}
	if !res.OK {
		h.auditDesk(r, authn, tenant, peerID, "peer.desk.get", map[string]any{"path": path, "ok": false, "error": res.Error})
		writeError(w, deskResultStatus(res), errors.New(deskResultError(res)))
		return
	}
	h.auditDesk(r, authn, tenant, peerID, "peer.desk.get", map[string]any{"path": path, "ok": true, "bytes": len(res.Data)})
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+deskFilename(path)+`"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(res.Data)
}

func (h *HTTPServer) apiDeskTermStart(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.term") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	var body struct {
		Rows int `json:"rows"`
		Cols int `json:"cols"`
	}
	if !decodeDeskJSON(w, r, 4<<10, &body) {
		return
	}
	if body.Rows <= 0 {
		body.Rows = 24
	}
	if body.Cols <= 0 {
		body.Cols = 80
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	opID := uuid.NewString()
	res, err := h.coord.DeskCall(ctx, peerID, operate.Frame{
		OpID: opID,
		Kind: operate.KindPTYStart,
		Rows: body.Rows,
		Cols: body.Cols,
	})
	if err != nil {
		if !deskLookupErr(err) {
			h.auditDesk(r, authn, tenant, peerID, "peer.desk.term", map[string]any{"ok": false, "error": err.Error()})
		}
		writeDeskErr(w, err)
		return
	}
	if !res.OK {
		h.auditDesk(r, authn, tenant, peerID, "peer.desk.term", map[string]any{"ok": false, "error": res.Error})
		writeError(w, deskResultStatus(res), errors.New(deskResultError(res)))
		return
	}
	h.auditDesk(r, authn, tenant, peerID, "peer.desk.term", map[string]any{"ok": true, "opId": opID})
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "opId": opID})
}

func (h *HTTPServer) apiDeskTermIn(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.term") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	var body struct {
		OpID string `json:"opId"`
		Data string `json:"data"`
		Rows int    `json:"rows"`
		Cols int    `json:"cols"`
	}
	if !decodeDeskJSON(w, r, 64<<10, &body) {
		return
	}
	if body.OpID == "" {
		writeError(w, http.StatusBadRequest, errors.New("opId is required"))
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	sub, err := h.coord.DeskSubscribe(ctx, peerID)
	if err != nil {
		writeDeskErr(w, err)
		return
	}
	if sub.OpID != body.OpID {
		writeError(w, http.StatusConflict, errors.New("terminal session mismatch"))
		return
	}
	select {
	case <-sub.Finished:
		writeError(w, http.StatusConflict, errors.New("terminal session ended"))
		return
	default:
	}
	if body.Rows > 0 && body.Cols > 0 {
		if err := h.coord.DeskPush(ctx, peerID, operate.Frame{
			OpID: body.OpID, Kind: operate.KindPTYResize, Rows: body.Rows, Cols: body.Cols,
		}); err != nil {
			writeDeskErr(w, err)
			return
		}
	}
	if body.Data != "" {
		if err := h.coord.DeskPush(ctx, peerID, operate.Frame{
			OpID: body.OpID, Kind: operate.KindPTYData, Data: []byte(body.Data),
		}); err != nil {
			writeDeskErr(w, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *HTTPServer) apiDeskTermClose(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.term") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	sub, err := h.coord.DeskSubscribe(ctx, peerID)
	if err != nil {
		writeDeskErr(w, err)
		return
	}
	if err := h.coord.DeskPush(ctx, peerID, operate.Frame{OpID: sub.OpID, Kind: operate.KindPTYClose}); err != nil {
		h.auditDesk(r, authn, tenant, peerID, "peer.desk.term.close", map[string]any{"ok": false, "opId": sub.OpID, "error": err.Error()})
		writeDeskErr(w, err)
		return
	}
	h.auditDesk(r, authn, tenant, peerID, "peer.desk.term.close", map[string]any{"ok": true, "opId": sub.OpID})
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *HTTPServer) apiDeskTermOut(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, id string) {
	if !h.requireAdmin(w, r, authn, tenant, "peer.desk.term") {
		return
	}
	peerID, ok := parseDeskPeer(w, id)
	if !ok {
		return
	}
	ctx, cancel := h.deskContext(r, tenant.ID)
	defer cancel()
	sub, err := h.coord.DeskSubscribe(ctx, peerID)
	if err != nil {
		writeDeskErr(w, err)
		return
	}
	if want := r.URL.Query().Get("op"); want != "" && want != sub.OpID {
		writeError(w, http.StatusConflict, errors.New("terminal session mismatch"))
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	clearStreamDeadlines(w)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	keepalive := time.NewTicker(watchKeepaliveInterval)
	defer keepalive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepalive.C:
			if _, err := io.WriteString(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
		case <-sub.Finished:
			drainDeskBytes(w, flusher, sub)
			_ = writeSSEEvent(w, "exit", map[string]any{"opId": sub.OpID})
			flusher.Flush()
			return
		case chunk := <-sub.Bytes:
			if !writeDeskData(w, flusher, sub.OpID, chunk) {
				return
			}
		}
	}
}

func drainDeskBytes(w http.ResponseWriter, flusher http.Flusher, sub operate.PTYSub) {
	for {
		select {
		case chunk := <-sub.Bytes:
			if !writeDeskData(w, flusher, sub.OpID, chunk) {
				return
			}
		default:
			return
		}
	}
}

func writeDeskData(w http.ResponseWriter, flusher http.Flusher, opID string, chunk []byte) bool {
	if err := writeSSEEvent(w, "data", map[string]any{
		"opId": opID,
		"data": base64.StdEncoding.EncodeToString(chunk),
	}); err != nil {
		return false
	}
	flusher.Flush()
	return true
}

func (h *HTTPServer) finishDesk(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, peerID uuid.UUID, action string, diff map[string]any, res operate.Result, err error) {
	if err != nil {
		if !deskLookupErr(err) {
			diff["ok"] = false
			diff["error"] = err.Error()
			h.auditDesk(r, authn, tenant, peerID, action, diff)
		}
		writeDeskErr(w, err)
		return
	}
	if !res.OK {
		diff["ok"] = false
		diff["error"] = res.Error
		h.auditDesk(r, authn, tenant, peerID, action, diff)
		writeError(w, deskResultStatus(res), errors.New(deskResultError(res)))
		return
	}
	diff["ok"] = true
	h.auditDesk(r, authn, tenant, peerID, action, diff)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *HTTPServer) deskContext(r *http.Request, tenantID uuid.UUID) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(r.Context(), deskCallMax)
	return handlers.WithKnownTenantID(ctx, tenantID), cancel
}

func (h *HTTPServer) deskActor(r *http.Request, authn *authnContext, tenant *repo.Tenant) string {
	if authn != nil && authn.claims != nil && h.pool != nil && tenant != nil {
		var email string
		err := db.WithTenant(r.Context(), h.pool, tenant.ID, func(q db.Querier) error {
			user, gerr := repo.NewUsers(q).GetByID(r.Context(), authn.claims.UserID)
			if gerr != nil {
				return gerr
			}
			email = user.Email
			return nil
		})
		if err == nil && email != "" {
			return email
		}
		slog.Warn("desk actor email", "err", err)
	}
	if authn != nil && authn.apiToken != nil {
		return "api-token"
	}
	return "admin"
}

func (h *HTTPServer) auditDesk(r *http.Request, authn *authnContext, tenant *repo.Tenant, peerID uuid.UUID, action string, diff map[string]any) {
	if tenant == nil {
		return
	}
	raw, err := json.Marshal(diff)
	if err != nil {
		slog.Warn("desk audit", "err", err)
		return
	}
	ev := &repo.AuditEvent{
		TenantID:     &tenant.ID,
		ActorType:    "system",
		Action:       action,
		ResourceType: "peer",
		ResourceID:   &peerID,
		Diff:         raw,
	}
	if authn != nil && authn.claims != nil {
		id := authn.claims.UserID
		ev.ActorType = "user"
		ev.ActorID = &id
	} else if authn != nil && authn.apiToken != nil {
		ev.ActorType = "api"
	}
	ua := r.UserAgent()
	if ua != "" {
		ev.UserAgent = &ua
	}
	h.auditInTenant(r.Context(), tenant.ID, ev)
}

func parseDeskPeer(w http.ResponseWriter, id string) (uuid.UUID, bool) {
	peerID, err := uuid.Parse(id)
	if err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid peer id"))
		return uuid.Nil, false
	}
	return peerID, true
}

func decodeDeskJSON(w http.ResponseWriter, r *http.Request, max int64, dest any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dest); err != nil {
		writeError(w, http.StatusBadRequest, errors.New("invalid json"))
		return false
	}
	return true
}

func writeDeskErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, operate.ErrOffline), errors.Is(err, operate.ErrNoSession):
		writeError(w, http.StatusConflict, err)
	case errors.Is(err, operate.ErrExecOff):
		writeError(w, http.StatusForbidden, err)
	case errors.Is(err, operate.ErrBusy):
		writeError(w, http.StatusTooManyRequests, err)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(w, http.StatusGatewayTimeout, err)
	case errors.Is(err, context.Canceled):
		writeError(w, http.StatusRequestTimeout, errors.New("request canceled"))
	default:
		if _, ok := status.FromError(err); ok {
			writeGRPCError(w, err)
			return
		}
		writeError(w, http.StatusBadGateway, err)
	}
}

func deskLookupErr(err error) bool {
	st, ok := status.FromError(err)
	return ok && (st.Code() == codes.NotFound || st.Code() == codes.InvalidArgument)
}

func deskResultStatus(res operate.Result) int {
	switch res.Error {
	case "exec disabled":
		return http.StatusForbidden
	case "not found":
		return http.StatusNotFound
	default:
		return http.StatusBadRequest
	}
}

func deskResultError(res operate.Result) string {
	if res.Error != "" {
		return res.Error
	}
	return "device rejected the request"
}

func deskFilename(path string) string {
	name := filepath.Base(path)
	name = strings.Map(func(r rune) rune {
		if r < 32 || r == '"' || r == '\\' || r == '/' {
			return -1
		}
		return r
	}, name)
	if name == "" || name == "." || name == ".." {
		return "download"
	}
	return name
}
