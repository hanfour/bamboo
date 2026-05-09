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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"

	"github.com/coder/websocket"
)

// Server is the in-process router.
type Server struct {
	log      *slog.Logger
	mu       sync.RWMutex
	sessions map[[PubKeyLen]byte]*session
}

func New(log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		log:      log,
		sessions: make(map[[PubKeyLen]byte]*session),
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
	defer c.Close(websocket.StatusInternalError, "handler exit")

	ctx := r.Context()
	if err := s.runSession(ctx, c); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Info("session ended", "err", err, "remote", r.RemoteAddr)
	}
}

// session holds one connected client's state.
type session struct {
	conn *websocket.Conn
	key  [PubKeyLen]byte
	send chan []byte
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
	// TODO(JJJJ-followup): verify ch.AuthToken as a controller-issued
	// JWT and partition the session map by tenant. v0 accepts any
	// CLIENT_HELLO and uses a single global namespace.

	sess := &session{conn: c, key: ch.SrcKey, send: make(chan []byte, 64)}
	s.register(sess)
	defer s.unregister(sess)

	hello, err := Encode(Frame{Type: TypeServerHello, Payload: []byte{0x00}}) // version 0
	if err != nil {
		return err
	}
	if err := c.Write(ctx, websocket.MessageBinary, hello); err != nil {
		return fmt.Errorf("write server_hello: %w", err)
	}

	s.log.Info("client connected", "key", fmt.Sprintf("%x", ch.SrcKey[:6]))

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
// pkt.DstKey. When the destination isn't connected we send a
// PEER_GONE back so the sender can stop trying.
func (s *Server) forward(sender *session, pkt PacketFrame) {
	s.mu.RLock()
	dst := s.sessions[pkt.DstKey]
	dstFound := dst != nil
	s.mu.RUnlock()
	s.log.Debug("forward",
		"src", fmt.Sprintf("%x", sender.key[:6]),
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
	out := EncodePacket(sender.key, pkt.Body)
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
