// SPDX-License-Identifier: AGPL-3.0-or-later

// Package server implements the bamboo relay's frame router. See ADR
// 0013 for the protocol.
//
// v0 (this PR): in-memory pubkey -> connection map, no auth, single
// tenant. Each connected client is identified by the 32-byte WG
// pubkey it presents in CLIENT_HELLO. PACKET frames are forwarded to
// the destination's connection if known, or dropped (with PEER_GONE
// returned to the sender) otherwise.
package server

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
	"github.com/hanfour/bamboo/apps/relay/auth"
)

// Options configures a relay Server. Zero-value Options is allowed in
// dev mode (--dev-no-auth) but in production SharedSecret should be
// non-empty so the relay refuses any CLIENT_HELLO without a valid
// controller-issued JWT.
type Options struct {
	Log          *slog.Logger
	SharedSecret []byte // HMAC key shared with the controller's relay-token issuer
	AllowNoAuth  bool   // if true, accept any CLIENT_HELLO (single global tenant)
}

// Server is the in-process router. Sessions are partitioned by
// tenant_id from the verified token so cross-tenant traffic is
// impossible.
type Server struct {
	log         *slog.Logger
	secret      []byte
	allowNoAuth bool

	mu       sync.RWMutex
	sessions map[sessionKey]*session
}

// sessionKey scopes a connection to one tenant. Two peers in
// different tenants can hold identical pubkeys without collision.
type sessionKey struct {
	tenant string
	pubkey [PubKeyLen]byte
}

// New constructs a relay Server. Pass AllowNoAuth=true only in dev;
// production must supply SharedSecret matching the controller's
// session_secret so JWTs verify.
func New(opts Options) *Server {
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:         log,
		secret:      opts.SharedSecret,
		allowNoAuth: opts.AllowNoAuth,
		sessions:    make(map[sessionKey]*session),
	}
}

// HandleRelay is the HTTP handler that upgrades to WebSocket and runs
// one client session.
func (s *Server) HandleRelay(w http.ResponseWriter, r *http.Request) {
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: true, // dev: any origin accepted
	})
	if err != nil {
		s.log.Warn("ws accept failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	defer func() { _ = c.Close(websocket.StatusInternalError, "handler exit") }()

	ctx := r.Context()
	if err := s.runSession(ctx, c); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Info("session ended", "err", err, "remote", r.RemoteAddr)
	}
}

// session holds one connected client's state.
type session struct {
	conn   *websocket.Conn
	key    sessionKey
	pubkey [PubKeyLen]byte // shortcut for the pubkey portion of key
	send   chan []byte
}

func (s *Server) runSession(ctx context.Context, c *websocket.Conn) error {
	// Protocol: client speaks first with CLIENT_HELLO carrying its
	// pubkey + auth token. The server replies with SERVER_HELLO
	// (= "registered, you can send PACKET frames now"). This ordering
	// gives the client a synchronous signal that it is in the routing
	// map — without it, two clients connecting back-to-back can race
	// each other on the first packet.
	_, payload, err := c.Read(ctx)
	if err != nil {
		return fmt.Errorf("read client_hello: %w", err)
	}
	frame, err := Decode(payload)
	if err != nil {
		return fmt.Errorf("decode client_hello: %w", err)
	}
	if frame.Type != TypeClientHello {
		return fmt.Errorf("expected client_hello, got 0x%02x", frame.Type)
	}
	ch, err := ParseClientHello(frame.Payload)
	if err != nil {
		return err
	}

	tenantID, err := s.verifyAuth(ch)
	if err != nil {
		s.log.Warn("auth rejected", "err", err)
		return fmt.Errorf("auth: %w", err)
	}

	sess := &session{
		conn:   c,
		key:    sessionKey{tenant: tenantID, pubkey: ch.SrcKey},
		pubkey: ch.SrcKey,
		send:   make(chan []byte, 64),
	}
	s.register(sess)
	defer s.unregister(sess)

	hello, err := Encode(Frame{Type: TypeServerHello, Payload: []byte{0x00}}) // version 0
	if err != nil {
		return err
	}
	if err := c.Write(ctx, websocket.MessageBinary, hello); err != nil {
		return fmt.Errorf("write server_hello: %w", err)
	}

	s.log.Info("client connected",
		"tenant", tenantID,
		"key", fmt.Sprintf("%x", ch.SrcKey[:6]),
	)

	// Two goroutines: one reads from the wire and routes outbound
	// PACKET frames, the other drains sess.send -> wire.
	errCh := make(chan error, 2)
	go func() { errCh <- s.readLoop(ctx, sess) }()
	go func() { errCh <- s.writeLoop(ctx, sess) }()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Server) readLoop(ctx context.Context, sess *session) error {
	for {
		_, payload, err := sess.conn.Read(ctx)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		frame, err := Decode(payload)
		if err != nil {
			s.log.Warn("malformed frame", "err", err)
			continue
		}
		switch frame.Type {
		case TypePacket:
			pkt, err := ParsePacket(frame.Payload)
			if err != nil {
				s.log.Warn("malformed packet", "err", err)
				continue
			}
			s.forward(sess, pkt)
		case TypeKeepalive:
			// no-op
		default:
			s.log.Warn("unexpected frame", "type", frame.Type)
		}
	}
}

func (s *Server) writeLoop(ctx context.Context, sess *session) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case msg, ok := <-sess.send:
			if !ok {
				return nil
			}
			if err := sess.conn.Write(ctx, websocket.MessageBinary, msg); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
	}
}

// forward routes a packet from sess to the connection that owns
// pkt.DstKey, scoped to sender's tenant. When the destination isn't
// connected (or is in a different tenant) we send a PEER_GONE back
// so the sender can stop trying.
func (s *Server) forward(sender *session, pkt PacketFrame) {
	dstKey := sessionKey{tenant: sender.key.tenant, pubkey: pkt.DstKey}
	s.mu.RLock()
	dst := s.sessions[dstKey]
	dstFound := dst != nil
	s.mu.RUnlock()
	s.log.Debug("forward",
		"tenant", sender.key.tenant,
		"src", fmt.Sprintf("%x", sender.pubkey[:6]),
		"dst", fmt.Sprintf("%x", pkt.DstKey[:6]),
		"found", dstFound,
		"body_len", len(pkt.Body),
	)

	if dst == nil {
		buf, err := Encode(Frame{Type: TypePeerGone, Payload: pkt.DstKey[:]})
		if err != nil {
			return
		}
		select {
		case sender.send <- buf:
		default:
			// Sender's queue is full; drop. The sender will retry.
		}
		return
	}

	// Re-encode so the destination receives a complete TypePacket
	// frame whose source key it can identify by the sender field.
	// Including the sender key lets the receiver build the inbound
	// path back without a separate lookup; we encode it as the
	// "destination" of the response packet payload from the
	// destination's point of view.
	out := EncodePacket(sender.pubkey, pkt.Body)
	buf, err := Encode(out)
	if err != nil {
		return
	}
	select {
	case dst.send <- buf:
	default:
		// Destination's queue is full; drop. WireGuard's retry will
		// recover; better than blocking the sender.
	}
}

func (s *Server) register(sess *session) {
	s.mu.Lock()
	if existing, ok := s.sessions[sess.key]; ok {
		// A new connection from the same key replaces the old one
		// (e.g. client reconnect after suspend). Close the old send
		// channel so its writeLoop exits.
		close(existing.send)
	}
	s.sessions[sess.key] = sess
	s.mu.Unlock()
}

func (s *Server) unregister(sess *session) {
	s.mu.Lock()
	if cur, ok := s.sessions[sess.key]; ok && cur == sess {
		delete(s.sessions, sess.key)
		close(sess.send)
	}
	s.mu.Unlock()
}

// verifyAuth verifies the CLIENT_HELLO auth token. Returns the
// tenant_id the connection should be scoped to. In --dev-no-auth
// mode the token is ignored and every connection lands in tenant
// "dev" so existing test paths still work.
func (s *Server) verifyAuth(ch ClientHello) (string, error) {
	if s.allowNoAuth {
		return "dev", nil
	}
	if len(s.secret) == 0 {
		return "", errors.New("server has no shared secret configured")
	}
	if ch.AuthToken == "" {
		return "", errors.New("client_hello missing auth token")
	}
	claims, err := auth.VerifyRelayToken(s.secret, ch.AuthToken)
	if err != nil {
		return "", err
	}
	// Bind the token's wg pubkey to the CLIENT_HELLO src key —
	// otherwise a peer could redeem its own token and impersonate any
	// pubkey it likes.
	want, err := base64.StdEncoding.DecodeString(claims.WGPublicKey)
	if err != nil || len(want) != PubKeyLen {
		return "", errors.New("token wg pubkey malformed")
	}
	if subtle.ConstantTimeCompare(want, ch.SrcKey[:]) != 1 {
		return "", errors.New("client_hello pubkey does not match token")
	}
	return claims.TenantID, nil
}
