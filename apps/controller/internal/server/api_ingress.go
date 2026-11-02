// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/db"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// Public services ingress admin REST
// (docs/design/2026-11-02-public-services-ingress.md §4.2). Create is open
// to any tenant member (it only files a pending request); approve, patch,
// delete, and token issue are admin-only.

const ingressReqMaxBytes = 1 << 16

type ingressServiceJSON struct {
	ID           string     `json:"id"`
	PeerID       string     `json:"peerId"`
	PeerHostname string     `json:"peerHostname"`
	PeerIP       string     `json:"peerIp"`
	Label        string     `json:"label"`
	URL          string     `json:"url"`
	TargetPort   int        `json:"targetPort"`
	Enabled      bool       `json:"enabled"`
	Approved     bool       `json:"approved"`
	ApprovedAt   *time.Time `json:"approvedAt,omitempty"`
	Live         bool       `json:"live"`
	ACLRule      string     `json:"aclRule,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type ingressTokenInfoJSON struct {
	PeerID    string    `json:"peerId"`
	IssuedAt  time.Time `json:"issuedAt"`
	ExpiresAt time.Time `json:"expiresAt"`
}

type ingressListJSON struct {
	Services []ingressServiceJSON  `json:"services"`
	Suffix   string                `json:"suffix"`
	Token    *ingressTokenInfoJSON `json:"token"`
}

type apiCreateIngressReq struct {
	Label      string `json:"label"`
	TargetPort int    `json:"target_port"`
}

type apiPatchIngressReq struct {
	Enabled    *bool `json:"enabled"`
	TargetPort *int  `json:"target_port"`
}

// errIngressNotFound is the in-transaction sentinel for "missing or in
// another tenant"; both answer 404.
var errIngressNotFound = errors.New("ingress service not found")

func (h *HTTPServer) ingressServiceToJSON(s *repo.IngressService) ingressServiceJSON {
	out := ingressServiceJSON{
		ID: s.ID.String(), PeerID: s.PeerID.String(), PeerHostname: s.PeerHostname, PeerIP: s.PeerIP,
		Label: s.Label, TargetPort: s.TargetPort, Enabled: s.Enabled, Approved: s.Approved(),
		ApprovedAt: s.ApprovedAt, Live: s.Live(), CreatedAt: s.CreatedAt, UpdatedAt: s.UpdatedAt,
	}
	if h.ingressSuffix != "" {
		out.URL = "https://" + s.Label + "." + h.ingressSuffix
	}
	if out.Live {
		out.ACLRule = fmt.Sprintf("tag:%s -> %s:%d", policy.IngressTag, s.PeerIP, s.TargetPort)
	}
	return out
}

// ingressActorID is the user behind the request, when there is one.
func ingressActorID(authn *authnContext) *uuid.UUID {
	switch {
	case authn == nil:
		return nil
	case authn.claims != nil:
		id := authn.claims.UserID
		return &id
	case authn.apiToken != nil && authn.apiToken.CreatedBy != nil:
		id := *authn.apiToken.CreatedBy
		return &id
	}
	return nil
}

// ingressApproverID is what approved_by records; uuid.Nil when the
// approval came without a user identity (dev fallback).
func ingressApproverID(authn *authnContext) uuid.UUID {
	if id := ingressActorID(authn); id != nil {
		return *id
	}
	return uuid.Nil
}

// routeIngress handles /api/v1/ingress[/token|/{sid}[/approve]].
func (h *HTTPServer) routeIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant) bool {
	if r.URL.Path == "/api/v1/ingress" {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiListIngress(w, r, tenant)
		return true
	}
	rest, ok := strings.CutPrefix(r.URL.Path, "/api/v1/ingress/")
	if !ok || rest == "" {
		return false
	}
	if rest == "token" {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiIssueIngressToken(w, r, authn, tenant)
		return true
	}
	if sid, found := strings.CutSuffix(rest, "/approve"); found && sid != "" && !strings.Contains(sid, "/") {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return true
		}
		h.apiApproveIngress(w, r, authn, tenant, sid)
		return true
	}
	if !strings.Contains(rest, "/") {
		switch r.Method {
		case http.MethodPatch:
			h.apiPatchIngress(w, r, authn, tenant, rest)
		case http.MethodDelete:
			h.apiDeleteIngress(w, r, authn, tenant, rest)
		default:
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		}
		return true
	}
	writeRouteMissing(w)
	return true
}

func (h *HTTPServer) apiListIngress(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant) {
	out := ingressListJSON{Services: []ingressServiceJSON{}, Suffix: h.ingressSuffix}
	err := db.WithTenant(r.Context(), h.pool, tenant.ID, func(q db.Querier) error {
		list, err := repo.NewIngressServices(q).ListByTenant(r.Context(), tenant.ID)
		if err != nil {
			return err
		}
		for _, s := range list {
			out.Services = append(out.Services, h.ingressServiceToJSON(s))
		}
		tok, err := repo.NewIngressTokens(q).Get(r.Context(), tenant.ID)
		if errors.Is(err, repo.ErrNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		out.Token = &ingressTokenInfoJSON{PeerID: tok.PeerID.String(), IssuedAt: tok.IssuedAt, ExpiresAt: tok.ExpiresAt}
		return nil
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// apiCreateIngress implements POST /api/v1/peers/{id}/ingress.
func (h *HTTPServer) apiCreateIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, peerIDStr string) {
	peerID, err := uuid.Parse(peerIDStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var req apiCreateIngressReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ingressReqMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	req.Label = strings.ToLower(strings.TrimSpace(req.Label))
	if err := repo.ValidateIngressLabel(req.Label); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	if err := repo.ValidateIngressPort(req.TargetPort); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	ctx := r.Context()
	var (
		created *repo.IngressService
		auditEv *repo.AuditEvent
		auditOK bool
	)
	txErr := db.WithTenant(ctx, h.pool, tenant.ID, func(q db.Querier) error {
		peer, ok, gerr := getPeerInTenant(ctx, q, tenant.ID, peerID)
		if gerr != nil {
			return gerr
		}
		if !ok {
			return errIngressNotFound
		}
		svcs := repo.NewIngressServices(q)
		taken, terr := svcs.LabelTaken(ctx, req.Label)
		if terr != nil {
			return terr
		}
		if taken {
			return repo.ErrIngressLabelTaken
		}
		s, cerr := svcs.Create(ctx, tenant.ID, peerID, req.Label, req.TargetPort, ingressActorID(authn))
		if cerr != nil {
			return cerr
		}
		created = s
		auditEv, auditOK = writePeerAuditTx(ctx, q, authn, tenant.ID, peerID, "ingress.create", map[string]any{
			"service_id": s.ID, "label": s.Label, "target_port": s.TargetPort, "hostname": peer.Hostname,
		})
		return nil
	})
	switch {
	case errors.Is(txErr, errIngressNotFound):
		http.NotFound(w, r)
		return
	case errors.Is(txErr, repo.ErrIngressLabelTaken):
		writeError(w, http.StatusConflict, txErr)
		return
	case txErr != nil:
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	h.emitAuditHook(ctx, auditEv, auditOK)
	// A new service is pending and disabled, never live: no bump, so a
	// member filing requests cannot make the whole tenant re-register.
	writeJSON(w, http.StatusCreated, h.ingressServiceToJSON(created))
}

// loadIngressInTenant fetches a service inside the tenant tx; another
// tenant's row (or a missing one) is errIngressNotFound.
func loadIngressInTenant(ctx context.Context, svcs *repo.IngressServices, tenantID, id uuid.UUID) (*repo.IngressService, error) {
	s, err := svcs.GetByID(ctx, id)
	if errors.Is(err, repo.ErrNotFound) || (err == nil && s.TenantID != tenantID) {
		return nil, errIngressNotFound
	}
	return s, err
}

func (h *HTTPServer) apiApproveIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, sidStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.approve") {
		return
	}
	h.mutateIngress(w, r, tenant, sidStr, func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error) {
		if cur.Approved() {
			return nil, nil, nil // keep the original approver; no audit, no bump
		}
		if _, err := svcs.Approve(ctx, cur.ID, ingressApproverID(authn)); err != nil {
			return nil, nil, err
		}
		ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, "ingress.approve", map[string]any{
			"service_id": cur.ID, "label": cur.Label, "target_port": cur.TargetPort,
		})
		return []*repo.AuditEvent{ev}, []bool{ok}, nil
	})
}

func (h *HTTPServer) apiPatchIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, sidStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.update") {
		return
	}
	var req apiPatchIngressReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, ingressReqMaxBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("decode body: %w", err))
		return
	}
	if req.Enabled == nil && req.TargetPort == nil {
		writeError(w, http.StatusBadRequest, errors.New("nothing to update: send enabled and/or target_port"))
		return
	}
	if req.TargetPort != nil {
		if err := repo.ValidateIngressPort(*req.TargetPort); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
	}
	h.mutateIngress(w, r, tenant, sidStr, func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error) {
		var evs []*repo.AuditEvent
		var oks []bool
		if req.TargetPort != nil && *req.TargetPort != cur.TargetPort {
			if _, err := svcs.SetTargetPort(ctx, cur.ID, *req.TargetPort); err != nil {
				return nil, nil, err
			}
			ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, "ingress.update", map[string]any{
				"service_id": cur.ID, "label": cur.Label,
				"target_port": map[string]any{"from": cur.TargetPort, "to": *req.TargetPort}, "approval_reset": true,
			})
			evs, oks = append(evs, ev), append(oks, ok)
		}
		if req.Enabled != nil && *req.Enabled != cur.Enabled {
			if _, err := svcs.SetEnabled(ctx, cur.ID, *req.Enabled); err != nil {
				return nil, nil, err
			}
			action := "ingress.disable"
			if *req.Enabled {
				action = "ingress.enable"
			}
			ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, action, map[string]any{
				"service_id": cur.ID, "label": cur.Label,
			})
			evs, oks = append(evs, ev), append(oks, ok)
		}
		return evs, oks, nil
	})
}

func (h *HTTPServer) apiDeleteIngress(w http.ResponseWriter, r *http.Request, authn *authnContext, tenant *repo.Tenant, sidStr string) {
	if !h.requireAdmin(w, r, authn, tenant, "ingress.delete") {
		return
	}
	h.mutateIngress(w, r, tenant, sidStr, func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error) {
		if _, err := svcs.Delete(ctx, cur.ID); err != nil {
			return nil, nil, err
		}
		ev, ok := writePeerAuditTx(ctx, q, authn, tenant.ID, cur.PeerID, "ingress.delete", map[string]any{
			"service_id": cur.ID, "label": cur.Label, "target_port": cur.TargetPort,
		})
		return []*repo.AuditEvent{ev}, []bool{ok}, nil
	})
}

type ingressMutation func(ctx context.Context, q db.Querier, svcs *repo.IngressServices, cur *repo.IngressService) ([]*repo.AuditEvent, []bool, error)

// mutateIngress is the shared shape of approve/patch/delete: load the
// row in the tenant tx, apply fn (which writes its own audit rows), read
// the row back, emit audit hooks after commit, bump the policy revision
// when liveness changed (Task 2's generated rules), and answer.
func (h *HTTPServer) mutateIngress(w http.ResponseWriter, r *http.Request, tenant *repo.Tenant, sidStr string, fn ingressMutation) {
	sid, err := uuid.Parse(sidStr)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	ctx := r.Context()
	var (
		before *repo.IngressService
		after  *repo.IngressService
		evs    []*repo.AuditEvent
		oks    []bool
	)
	txErr := db.WithTenant(ctx, h.pool, tenant.ID, func(q db.Querier) error {
		svcs := repo.NewIngressServices(q)
		cur, lerr := loadIngressInTenant(ctx, svcs, tenant.ID, sid)
		if lerr != nil {
			return lerr
		}
		before = cur
		e, o, ferr := fn(ctx, q, svcs, cur)
		if ferr != nil {
			return ferr
		}
		evs, oks = e, o
		upd, gerr := svcs.GetByID(ctx, sid)
		if errors.Is(gerr, repo.ErrNotFound) {
			return nil // deleted
		}
		after = upd
		return gerr
	})
	if errors.Is(txErr, errIngressNotFound) {
		http.NotFound(w, r)
		return
	}
	if txErr != nil {
		writeError(w, http.StatusInternalServerError, txErr)
		return
	}
	for i := range evs {
		h.emitAuditHook(ctx, evs[i], oks[i])
	}
	// Peers re-register only when the service's liveness flipped (a
	// deleted service is not live). Approving a disabled service or
	// changing a pending one's port does not wake the tenant's peers.
	if before.Live() != after.Live() && !h.bumpIngressPolicy(ctx, w, tenant.ID) {
		return
	}
	if after == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	writeJSON(w, http.StatusOK, h.ingressServiceToJSON(after))
}

// bumpIngressPolicy bumps the tenant's policy revision after a change
// that can affect public-service liveness, so connected peers
// re-register and pick up the generated rules (spec §4.4). The change
// has committed; on failure it answers 500 so the operator retries, like
// the route and exit-node paths. Returns false when it answered.
func (h *HTTPServer) bumpIngressPolicy(ctx context.Context, w http.ResponseWriter, tenantID uuid.UUID) bool {
	if h.coord == nil {
		return true
	}
	if _, err := h.coord.BumpPolicyRevision(ctx, tenantID); err != nil {
		slog.Warn("ingress: bump policy revision", "tenant_id", tenantID, "err", err)
		writeError(w, http.StatusInternalServerError, fmt.Errorf("bump policy revision: %w", err))
		return false
	}
	return true
}

// peerAffectsIngress reports whether an approval change or deletion of
// peer can change public-service liveness: it is the ingress peer or it
// hosts a service. Run it inside the tenant tx, before a delete (the
// services cascade away with the peer).
func peerAffectsIngress(ctx context.Context, q db.Querier, peer *repo.Peer) (bool, error) {
	if slices.Contains(peer.Tags, policy.IngressTag) {
		return true, nil
	}
	return repo.NewIngressServices(q).ExistsForPeer(ctx, peer.ID)
}
