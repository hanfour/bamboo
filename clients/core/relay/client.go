// SPDX-License-Identifier: Apache-2.0

// Package relay is the client-side proxy for the bamboo relay
// protocol (ADR 0013).
//
// One Client maintains a single WSS session to a relay server. For
// each remote peer addressable through the relay, the Client exposes
// a localhost UDP socket; WireGuard's per-peer Endpoint can be set to
// 127.0.0.1:<that port>. The Client multiplexes:
//
//	WG outbound:  WG -> per-peer UDP socket -> WSS PACKET frame
//	WG inbound:   WSS PACKET frame -> per-peer UDP socket (with src=loopback) -> WG's listen port
//
// Each per-peer socket is the natural demux key on both sides: WG
// associates an inbound packet with a peer by source UDP address, so
// we send from a different localhost port per peer.
package relay

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"

	"github.com/coder/websocket"
)

// PubKeyLen mirrors apps/relay/internal/server/frame.go.
const PubKeyLen = 32

// Frame types matching the relay protocol.
const (
	typeServerHello byte = 0x01
	typeClientHello byte = 0x02
	typePacket      byte = 0x03
	typePeerGone    byte = 0x04
	typeKeepalive   byte = 0x05
)

// Client is the established session.
type Client struct {
	log         *slog.Logger
	conn        *websocket.Conn
	wgListenUDP *net.UDPAddr // where WG-inbound packets get written
	selfKey     [PubKeyLen]byte

	mu    sync.Mutex
	peers map[[PubKeyLen]byte]*peerSocket

	stop context.CancelFunc
	done chan struct{}
}

type peerSocket struct {
	pubkey [PubKeyLen]byte
	conn   *net.UDPConn // bound on 127.0.0.1:auto; both reads from WG and writes to WG
	port   int          // local port the WG config should target
}

// Dial opens a WSS session to relayURL, sends CLIENT_HELLO with
// selfPubKey and token, waits for SERVER_HELLO. wgListenAddr is the
// loopback address (typically 127.0.0.1:<wg_port>) where WG inbound
// packets should be delivered. Caller is responsible for stopping
// the Client via Close().
func Dial(ctx context.Context, relayURL string, selfPubKey [PubKeyLen]byte, token string, wgListenAddr string) (*Client, error) {
	wgUDP, err := net.ResolveUDPAddr("udp", wgListenAddr)
	if err != nil {
		return nil, fmt.Errorf("resolve wg addr %q: %w", wgListenAddr, err)
	}

	conn, _, err := websocket.Dial(ctx, relayURL, nil)
	if err != nil {
		return nil, fmt.Errorf("ws dial %s: %w", relayURL, err)
	}

	// CLIENT_HELLO = 32-byte pubkey + token bytes.
	helloPayload := make([]byte, 0, PubKeyLen+len(token))
	helloPayload = append(helloPayload, selfPubKey[:]...)
	helloPayload = append(helloPayload, []byte(token)...)
	if err := writeFrame(ctx, conn, typeClientHello, helloPayload); err != nil {
		_ = conn.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("write client_hello: %w", err)
	}

	// SERVER_HELLO is the synchronous "you're registered" ack.
	typ, _, err := readFrame(ctx, conn)
	if err != nil {
		_ = conn.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("read server_hello: %w", err)
	}
	if typ != typeServerHello {
		_ = conn.Close(websocket.StatusInternalError, "")
		return nil, fmt.Errorf("expected server_hello, got 0x%02x", typ)
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	c := &Client{
		log:         slog.Default(),
		conn:        conn,
		wgListenUDP: wgUDP,
		selfKey:     selfPubKey,
		peers:       make(map[[PubKeyLen]byte]*peerSocket),
		stop:        cancel,
		done:        make(chan struct{}),
	}
	go c.readLoop(loopCtx)
	return c, nil
}

// AddPeer ensures a localhost UDP socket exists for peerPubKey and
// returns the host:port string the WireGuard config should use as the
// peer's Endpoint. Idempotent.
func (c *Client) AddPeer(peerPubKey [PubKeyLen]byte) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if existing, ok := c.peers[peerPubKey]; ok {
		return fmt.Sprintf("127.0.0.1:%d", existing.port), nil
	}

	// Bind on 127.0.0.1:0 so the OS picks an ephemeral port.
	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		return "", fmt.Errorf("bind per-peer udp: %w", err)
	}
	port := udp.LocalAddr().(*net.UDPAddr).Port
	ps := &peerSocket{pubkey: peerPubKey, conn: udp, port: port}
	c.peers[peerPubKey] = ps
	go c.peerOutboundLoop(ps)
	return fmt.Sprintf("127.0.0.1:%d", port), nil
}

// RemovePeer tears down the localhost socket for peerPubKey.
func (c *Client) RemovePeer(peerPubKey [PubKeyLen]byte) {
	c.mu.Lock()
	ps, ok := c.peers[peerPubKey]
	if ok {
		delete(c.peers, peerPubKey)
	}
	c.mu.Unlock()
	if ps != nil {
		_ = ps.conn.Close()
	}
}

// Close ends the WSS session and tears down every per-peer socket.
func (c *Client) Close() error {
	c.stop()
	c.mu.Lock()
	for _, ps := range c.peers {
		_ = ps.conn.Close()
	}
	c.peers = nil
	c.mu.Unlock()
	err := c.conn.Close(websocket.StatusNormalClosure, "client closed")
	<-c.done
	return err
}

// readLoop drains the WSS connection. Inbound PACKET frames carry
// (sourceKey, body); we look up the per-peer socket for that source
// and write the body to WG's listen address from that socket.
func (c *Client) readLoop(ctx context.Context) {
	defer close(c.done)
	for {
		typ, payload, err := readFrame(ctx, c.conn)
		if err != nil {
			if !errors.Is(err, context.Canceled) {
				c.log.Warn("relay read", "err", err)
			}
			return
		}
		switch typ {
		case typePacket:
			if len(payload) < PubKeyLen {
				continue
			}
			var srcKey [PubKeyLen]byte
			copy(srcKey[:], payload[:PubKeyLen])
			body := payload[PubKeyLen:]

			c.mu.Lock()
			ps := c.peers[srcKey]
			c.mu.Unlock()
			if ps == nil {
				// Peer not registered locally — drop. WG would have
				// no place to deliver it anyway.
				continue
			}
			if _, err := ps.conn.WriteToUDP(body, c.wgListenUDP); err != nil {
				c.log.Debug("write to wg", "err", err)
			}
		case typePeerGone:
			// Could surface this to a callback; for now just log so
			// users can debug "why isn't peer X reachable".
			if len(payload) >= PubKeyLen {
				c.log.Info("relay reports peer gone",
					"peer", fmt.Sprintf("%x", payload[:6]),
				)
			}
		case typeKeepalive:
			// no-op
		}
	}
}

// peerOutboundLoop reads from the per-peer socket (where WG sends
// outbound packets for that peer) and forwards them as PACKET frames
// over the WSS session.
func (c *Client) peerOutboundLoop(ps *peerSocket) {
	buf := make([]byte, 64*1024)
	for {
		n, _, err := ps.conn.ReadFromUDP(buf)
		if err != nil {
			// Socket closed during RemovePeer / Close; that's normal.
			return
		}
		body := make([]byte, PubKeyLen+n)
		copy(body[:PubKeyLen], ps.pubkey[:])
		copy(body[PubKeyLen:], buf[:n])
		if err := writeFrame(context.Background(), c.conn, typePacket, body); err != nil {
			c.log.Warn("relay write", "err", err)
			return
		}
	}
}

// MARK: framing

func writeFrame(ctx context.Context, conn *websocket.Conn, typ byte, payload []byte) error {
	total := 4 + 1 + len(payload)
	buf := make([]byte, total)
	binary.BigEndian.PutUint32(buf[0:4], uint32(total))
	buf[4] = typ
	copy(buf[5:], payload)
	return conn.Write(ctx, websocket.MessageBinary, buf)
}

func readFrame(ctx context.Context, conn *websocket.Conn) (byte, []byte, error) {
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return 0, nil, err
	}
	if len(raw) < 5 {
		return 0, nil, errors.New("frame too short")
	}
	declared := binary.BigEndian.Uint32(raw[0:4])
	if int(declared) != len(raw) {
		return 0, nil, fmt.Errorf("declared length %d != %d", declared, len(raw))
	}
	return raw[4], raw[5:], nil
}
