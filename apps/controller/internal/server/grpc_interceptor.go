// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"context"
	"strings"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// requireAuthUnaryInterceptor returns a gRPC unary interceptor that
// rejects unauthenticated calls with codes.Unauthenticated when
// require_auth=true. The whitelist covers methods whose own request
// carries a payload-borne credential (pre-auth-key, bearer field) or
// that are part of the unauthenticated onboarding / bootstrapping
// path:
//
//   - bamboo.v1.CoordinatorService/Register — credential lives on the
//     RegisterRequest payload (pre_auth_key_secret or bearer_token).
//     The CoordinatorHandler's resolveTenant rejects callers that
//     supply neither when requireAuth is on, so the interceptor does
//     not need to inspect the payload here.
//   - bamboo.v1.AuthService/{StartOIDCFlow,CompleteOIDCFlow,
//     RedeemPreAuthKey} — bootstrapping a session token.
//
// Heartbeat and WatchPeers used to be whitelisted on the assumption
// that the peer_id alone was a sufficient identifier; the doc's
// Finding #1 makes clear it isn't. They are now gated like every
// other authenticated method: the caller must present either a
// user-session JWT or a peer-session token issued at Register time
// (see authedAdapter on the CLI side). verifyBearer accepts both.
//
// require_auth=false (the default) returns a passthrough so all dev
// flows keep working unchanged. When sessionSec is empty the
// interceptor cannot verify tokens; it still rejects unauthenticated
// requests so the misconfiguration is loud.
func requireAuthUnaryInterceptor(requireAuth bool, sessionSec []byte, revoked *repo.RevokedSessions, users *repo.Users) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		if !requireAuth || isWhitelistedGRPCMethod(info.FullMethod) {
			return handler(ctx, req)
		}
		if err := verifyBearer(ctx, sessionSec, revoked, users); err != nil {
			return nil, err
		}
		return handler(ctx, req)
	}
}

// requireAuthStreamInterceptor mirrors the unary interceptor for
// streaming methods. WatchPeers is no longer whitelisted — see the
// unary interceptor's comment.
func requireAuthStreamInterceptor(requireAuth bool, sessionSec []byte, revoked *repo.RevokedSessions, users *repo.Users) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, info *grpc.StreamServerInfo, handler grpc.StreamHandler) error {
		if !requireAuth || isWhitelistedGRPCMethod(info.FullMethod) {
			return handler(srv, ss)
		}
		if err := verifyBearer(ss.Context(), sessionSec, revoked, users); err != nil {
			return err
		}
		return handler(srv, ss)
	}
}

// verifyBearer accepts both a user-session JWT and a peer-session
// token as a valid bearer. Domain separation in the HMAC inputs
// makes the two token types mutually unforgeable: only one verify
// path will accept any given signed body. Try peer-session first
// because heartbeat / watch are far more frequent than user-session
// calls and we want the common path fast.
//
// User-session JWTs additionally consult the revocation denylist
// (slice 3a) — a stolen Web cookie that hits a gRPC method gets the
// same rejection as one that hits a REST endpoint. Peer-session
// tokens do not go through the denylist; revoking a peer is done
// via the pre_auth_key / peer-row paths, which are independent.
func verifyBearer(ctx context.Context, sessionSec []byte, revoked *repo.RevokedSessions, users *repo.Users) error {
	md, _ := metadata.FromIncomingContext(ctx)
	var token string
	for _, v := range md.Get("authorization") {
		if strings.HasPrefix(v, "Bearer ") {
			token = strings.TrimSpace(strings.TrimPrefix(v, "Bearer "))
			break
		}
	}
	if token == "" {
		return status.Error(codes.Unauthenticated, "authentication required")
	}
	if len(sessionSec) == 0 {
		return status.Error(codes.Unauthenticated, "session signing not configured")
	}
	if _, err := auth.VerifyPeerSessionToken(sessionSec, token); err == nil {
		return nil
	}
	claims, err := auth.VerifySessionToken(sessionSec, token)
	if err != nil {
		return status.Error(codes.Unauthenticated, "invalid bearer token")
	}
	if revoked != nil && claims.JTI != uuid.Nil {
		isRev, ierr := revoked.IsRevoked(ctx, claims.JTI)
		if ierr != nil {
			return status.Errorf(codes.Internal, "revocation check: %v", ierr)
		}
		if isRev {
			return status.Error(codes.Unauthenticated, "session revoked")
		}
	}
	// Slice 3b: per-user session-version gate (matches the REST
	// authenticate path). Skipped when users repo isn't wired
	// (unit tests) — production always passes a non-nil repo.
	if users != nil {
		user, uerr := users.GetByID(ctx, claims.UserID)
		if uerr != nil {
			return status.Errorf(codes.Internal, "resolve user: %v", uerr)
		}
		if claims.SV < user.SessionVersion {
			return status.Error(codes.Unauthenticated, "session revoked (force sign-out)")
		}
	}
	return nil
}

func isWhitelistedGRPCMethod(fullMethod string) bool {
	switch fullMethod {
	case "/bamboo.v1.CoordinatorService/Register",
		"/bamboo.v1.AuthService/StartOIDCFlow",
		"/bamboo.v1.AuthService/CompleteOIDCFlow",
		"/bamboo.v1.AuthService/RedeemPreAuthKey":
		return true
	}
	return false
}
