// SPDX-License-Identifier: AGPL-3.0-or-later

// Package session is a device-to-device channel on top of the mesh.
//
// A peer proves it holds a WireGuard private key, and the listener
// accepts that key only when the operator put it in the allowlist.
// WireGuard AllowedIPs remain the network gate: a peer that cannot
// route to this listener never completes the handshake. Exec runs the
// argv directly and never through a shell. File paths stay inside Root.
package session

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/hanfour/bamboo/clients/core/encryption"
	"github.com/hanfour/bamboo/clients/core/wg"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

const (
	// DefaultPort is the TCP port a session listener uses.
	DefaultPort = 54110

	protocolVersion byte = 1
	kindHello       byte = 1
	kindRequest     byte = 2
	kindResponse    byte = 3

	// OpExec runs argv on the peer. The argv is not passed through a shell.
	OpExec = "exec"
	// OpPut writes a file inside the session root.
	OpPut = "put"
	// OpGet reads a file from the session root.
	OpGet = "get"
	// OpMsg appends a short text to the inbox.
	OpMsg = "msg"
	// OpPTY attaches to an interactive terminal on the peer.
	OpPTY = "pty"

	kindData    byte = 4
	kindWinsize byte = 5
	kindExit    byte = 6

	maxFrame   = 8 << 20
	maxFile    = 4 << 20
	maxText    = 4000
	maxArgs    = 32
	maxArgLen  = 4096
	maxOutput  = 256 << 10
	maxInbox   = 1 << 20
	timeSkew   = 2 * time.Minute
	headerSize = 1 + 1 + wg.KeySize + 4

	inboxName = "inbox.jsonl"
)

// Error strings returned inside a sealed response.
const (
	ErrDenied   = "denied"
	ErrPath     = "path not allowed"
	ErrExecOff  = "exec disabled"
	ErrBadReq   = "bad request"
	ErrInbox    = "inbox full"
	ErrNotFound = "not found"
	ErrOutput   = "output too large"
)

// Runner executes argv and returns its output. Exit codes are not errors.
type Runner func(ctx context.Context, argv []string) (stdout, stderr []byte, exitCode int, err error)

// Checker reports whether a peer public key may open a session.
// Implementations may change their answer while Serve is running.
type Checker interface {
	Allows(wg.PublicKey) bool
}

// AllowList is a Checker that replaces its whole set at once.
type AllowList struct {
	mu   sync.RWMutex
	keys map[wg.PublicKey]struct{}
}

// NewAllowList returns a list holding keys. Later Replace calls are
// visible to a Serve that was given this list as Config.Check.
func NewAllowList(keys []wg.PublicKey) *AllowList {
	a := &AllowList{}
	a.Replace(keys)
	return a
}

// Replace swaps the allowed public keys.
func (a *AllowList) Replace(keys []wg.PublicKey) {
	next := make(map[wg.PublicKey]struct{}, len(keys))
	for _, k := range keys {
		next[k] = struct{}{}
	}
	a.mu.Lock()
	a.keys = next
	a.mu.Unlock()
}

// Allows reports whether k is in the current set.
func (a *AllowList) Allows(k wg.PublicKey) bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	_, ok := a.keys[k]
	return ok
}

// Config is the listener policy. An empty Allow denies every handshake.
// When Check is set, it is used instead of Allow.
type Config struct {
	PrivateKey wg.PrivateKey
	Allow      []wg.PublicKey
	Check      Checker
	Root       string
	EnableExec bool
	Now        func() time.Time
	Run        Runner
}

// Request is one operation on an already-authenticated connection.
type Request struct {
	Op   string   `json:"op"`
	Argv []string `json:"argv,omitempty"`
	Path string   `json:"path,omitempty"`
	Data []byte   `json:"data,omitempty"`
	Text string   `json:"text,omitempty"`
	Rows int      `json:"rows,omitempty"`
	Cols int      `json:"cols,omitempty"`
}

// Response is the sealed result of a Request.
type Response struct {
	OK       bool   `json:"ok"`
	Error    string `json:"error,omitempty"`
	ExitCode int    `json:"exitCode,omitempty"`
	Stdout   []byte `json:"stdout,omitempty"`
	Stderr   []byte `json:"stderr,omitempty"`
	Data     []byte `json:"data,omitempty"`
}

// InboxEntry is one line of the local inbox.
type InboxEntry struct {
	From string `json:"from"`
	At   string `json:"at"`
	Text string `json:"text"`
}

type hello struct {
	Time  int64  `json:"time"`
	Nonce string `json:"nonce"`
}

// Serve accepts connections until ctx is cancelled or ln closes.
func Serve(ctx context.Context, ln net.Listener, cfg Config) error {
	if cfg.Root == "" {
		return errors.New("session root is required")
	}
	info, err := os.Stat(cfg.Root)
	if err != nil {
		return fmt.Errorf("session root: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("session root: %s is not a directory", cfg.Root)
	}
	s := &server{cfg: cfg, allow: allowSet(cfg.Allow)}
	if s.cfg.Run == nil {
		s.cfg.Run = defaultRunner
	}
	var wgWait sync.WaitGroup
	defer wgWait.Wait()
	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		wgWait.Add(1)
		go func() {
			defer wgWait.Done()
			defer func() { _ = conn.Close() }()
			connCtx, connCancel := context.WithCancel(ctx)
			defer connCancel()
			go func() {
				<-connCtx.Done()
				_ = conn.Close()
			}()
			s.handle(connCtx, conn)
		}()
	}
}

// Call dials addr, proves local, and runs one request against peer.
func Call(ctx context.Context, addr string, local wg.PrivateKey, peer wg.PublicKey, req Request) (Response, error) {
	var zero Response
	conn, err := dialAndHandshake(ctx, addr, local, peer)
	if err != nil {
		return zero, err
	}
	defer func() { _ = conn.Close() }()
	sealedReq, err := seal(peer, local, req)
	if err != nil {
		return zero, err
	}
	if err := writeFrame(conn, kindRequest, local.PublicKey(), sealedReq); err != nil {
		return zero, err
	}
	kind, remote, body, err := readFrame(conn)
	if err != nil {
		return zero, err
	}
	if kind != kindResponse || remote != peer {
		return zero, errors.New("bad response")
	}
	opened, err := open(peer, local, body)
	if err != nil {
		return zero, err
	}
	var resp Response
	if err := json.Unmarshal(opened, &resp); err != nil {
		return zero, err
	}
	if !resp.OK {
		if resp.Error == "" {
			resp.Error = ErrBadReq
		}
		return resp, fmt.Errorf("session: %s", resp.Error)
	}
	return resp, nil
}

// ReadInbox returns the messages stored under root. A missing inbox is empty.
func ReadInbox(root string) ([]InboxEntry, error) {
	path := filepath.Join(root, inboxName)
	f, err := os.Open(path) //nolint:gosec // G304: path is the session root plus a fixed name
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()
	var out []InboxEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), maxText+1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ent InboxEntry
		if err := json.Unmarshal(line, &ent); err != nil {
			return nil, fmt.Errorf("inbox: %w", err)
		}
		out = append(out, ent)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

type server struct {
	cfg   Config
	allow map[wg.PublicKey]struct{}
}

func (s *server) handle(ctx context.Context, conn net.Conn) {
	deadline := time.Now().Add(60 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetDeadline(deadline)
	kind, peer, body, err := readFrame(conn)
	if err != nil || kind != kindHello {
		return
	}
	if !s.allows(peer) {
		slog.Info("session denied", "peer", peer.Base64())
		return
	}
	opened, err := open(peer, s.cfg.PrivateKey, body)
	if err != nil {
		return
	}
	var hi hello
	if err := json.Unmarshal(opened, &hi); err != nil || hi.Nonce == "" {
		return
	}
	if skew(s.now(), time.Unix(hi.Time, 0)) {
		return
	}
	ack, err := seal(peer, s.cfg.PrivateKey, hello{Time: s.now().Unix(), Nonce: hi.Nonce})
	if err != nil {
		return
	}
	if err := writeFrame(conn, kindHello, s.cfg.PrivateKey.PublicKey(), ack); err != nil {
		return
	}
	kind, remote, body, err := readFrame(conn)
	if err != nil || kind != kindRequest || remote != peer {
		return
	}
	opened, err = open(peer, s.cfg.PrivateKey, body)
	if err != nil {
		return
	}
	var req Request
	if err := json.Unmarshal(opened, &req); err != nil {
		return
	}
	slog.Info("session", "op", req.Op, "peer", peer.Base64())
	if req.Op == OpPTY {
		_ = conn.SetDeadline(time.Time{})
		s.servePTY(ctx, conn, peer, req)
		return
	}
	resp := s.dispatch(ctx, peer, req)
	sealed, err := seal(peer, s.cfg.PrivateKey, resp)
	if err != nil {
		return
	}
	_ = writeFrame(conn, kindResponse, s.cfg.PrivateKey.PublicKey(), sealed)
}

func (s *server) dispatch(ctx context.Context, peer wg.PublicKey, req Request) Response {
	switch req.Op {
	case OpExec:
		return s.exec(ctx, req)
	case OpPut:
		return s.put(req)
	case OpGet:
		return s.get(req)
	case OpMsg:
		return s.msg(peer, req)
	default:
		return fail(ErrBadReq)
	}
}

func (s *server) exec(ctx context.Context, req Request) Response {
	if !s.cfg.EnableExec {
		return fail(ErrExecOff)
	}
	if len(req.Argv) == 0 || len(req.Argv) > maxArgs {
		return fail(ErrBadReq)
	}
	for _, a := range req.Argv {
		if a == "" || len(a) > maxArgLen || strings.ContainsRune(a, 0) {
			return fail(ErrBadReq)
		}
	}
	stdout, stderr, code, err := s.cfg.Run(ctx, req.Argv)
	if err != nil {
		if err.Error() == ErrOutput {
			return fail(ErrOutput)
		}
		return fail(ErrBadReq)
	}
	return Response{OK: true, ExitCode: code, Stdout: stdout, Stderr: stderr}
}

func (s *server) put(req Request) Response {
	if len(req.Data) > maxFile {
		return fail(ErrBadReq)
	}
	full, err := safePath(s.cfg.Root, req.Path)
	if err != nil {
		return fail(ErrPath)
	}
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		return fail(ErrBadReq)
	}
	if err := os.WriteFile(full, req.Data, 0o600); err != nil { //nolint:gosec // G306: mode 0600; path is safePath under the session root
		return fail(ErrBadReq)
	}
	return Response{OK: true}
}

func (s *server) get(req Request) Response {
	full, err := safePath(s.cfg.Root, req.Path)
	if err != nil {
		return fail(ErrPath)
	}
	st, err := os.Lstat(full)
	if errors.Is(err, os.ErrNotExist) {
		return fail(ErrNotFound)
	}
	if err != nil || st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return fail(ErrPath)
	}
	if st.Size() > maxFile {
		return fail(ErrBadReq)
	}
	data, err := os.ReadFile(full) //nolint:gosec // G304: full is safePath under the session root
	if err != nil {
		return fail(ErrBadReq)
	}
	return Response{OK: true, Data: data}
}

func (s *server) msg(peer wg.PublicKey, req Request) Response {
	return appendInbox(s.cfg.Root, s.now(), peer.Base64(), req.Text)
}

// appendInbox writes one inbox line. The mesh listener and the admin
// desk share a root, so the lock is per directory rather than per server.
func appendInbox(root string, now time.Time, from, text string) Response {
	text = strings.TrimSpace(text)
	if text == "" || len(text) > maxText || strings.ContainsRune(text, 0) {
		return fail(ErrBadReq)
	}
	if strings.TrimSpace(from) == "" || strings.ContainsRune(from, 0) {
		return fail(ErrBadReq)
	}
	ent := InboxEntry{
		From: from,
		At:   now.UTC().Format(time.RFC3339),
		Text: text,
	}
	line, err := json.Marshal(ent)
	if err != nil {
		return fail(ErrBadReq)
	}
	mu := inboxMu(root)
	mu.Lock()
	defer mu.Unlock()
	path := filepath.Join(root, inboxName)
	st, err := os.Stat(path)
	if err == nil && st.Size() > maxInbox {
		return fail(ErrInbox)
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fail(ErrBadReq)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600) //nolint:gosec // G304: fixed inbox name under the session root
	if err != nil {
		return fail(ErrBadReq)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(append(line, '\n')); err != nil {
		return fail(ErrBadReq)
	}
	return Response{OK: true}
}

var inboxMus sync.Map

func inboxMu(root string) *sync.Mutex {
	key := filepath.Clean(root)
	v, _ := inboxMus.LoadOrStore(key, &sync.Mutex{})
	mu, ok := v.(*sync.Mutex)
	if !ok {
		fresh := &sync.Mutex{}
		inboxMus.Store(key, fresh)
		return fresh
	}
	return mu
}

func (s *server) allows(k wg.PublicKey) bool {
	if s.cfg.Check != nil {
		return s.cfg.Check.Allows(k)
	}
	_, ok := s.allow[k]
	return ok
}

func (s *server) now() time.Time {
	if s.cfg.Now != nil {
		return s.cfg.Now()
	}
	return time.Now()
}

func defaultRunner(ctx context.Context, argv []string) ([]byte, []byte, int, error) {
	// argv comes from an allowlisted peer and is not interpreted by a shell.
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...) //nolint:gosec // G204: direct exec, no shell
	var stdout, stderr capBuf
	stdout.max = maxOutput
	stderr.max = maxOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.over || stderr.over {
		return nil, nil, 0, errors.New(ErrOutput)
	}
	if err == nil {
		return stdout.Bytes(), stderr.Bytes(), 0, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return stdout.Bytes(), stderr.Bytes(), exitErr.ExitCode(), nil
	}
	return nil, nil, 0, err
}

type capBuf struct {
	bytes.Buffer
	n    int
	max  int
	over bool
}

func (c *capBuf) Write(p []byte) (int, error) {
	if c.n+len(p) > c.max {
		c.over = true
		return 0, errors.New(ErrOutput)
	}
	n, err := c.Buffer.Write(p)
	c.n += n
	return n, err
}

func allowSet(keys []wg.PublicKey) map[wg.PublicKey]struct{} {
	out := make(map[wg.PublicKey]struct{}, len(keys))
	for _, k := range keys {
		out[k] = struct{}{}
	}
	return out
}

func fail(code string) Response {
	return Response{OK: false, Error: code}
}

func skew(a, b time.Time) bool {
	d := a.Sub(b)
	if d < 0 {
		d = -d
	}
	return d > timeSkew
}

func newNonce() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

func dialAndHandshake(ctx context.Context, addr string, local wg.PrivateKey, peer wg.PublicKey) (net.Conn, error) {
	dialer := net.Dialer{}
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	if err := clientHandshake(conn, local, peer); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func clientHandshake(conn net.Conn, local wg.PrivateKey, peer wg.PublicKey) error {
	nonce, err := newNonce()
	if err != nil {
		return err
	}
	sealedHello, err := seal(peer, local, hello{Time: time.Now().Unix(), Nonce: nonce})
	if err != nil {
		return err
	}
	if err := writeFrame(conn, kindHello, local.PublicKey(), sealedHello); err != nil {
		return err
	}
	kind, remote, body, err := readFrame(conn)
	if err != nil {
		return err
	}
	if kind != kindHello || remote != peer {
		return errors.New("handshake failed")
	}
	opened, err := open(peer, local, body)
	if err != nil {
		return errors.New("handshake failed")
	}
	var ack hello
	if err := json.Unmarshal(opened, &ack); err != nil || ack.Nonce != nonce {
		return errors.New("handshake failed")
	}
	if skew(time.Now(), time.Unix(ack.Time, 0)) {
		return errors.New("handshake failed")
	}
	return nil
}

func seal(peer wg.PublicKey, local wg.PrivateKey, v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return encryption.Encrypt(raw, wgtypes.Key(peer), wgtypes.Key(local))
}

func sealBytes(peer wg.PublicKey, local wg.PrivateKey, raw []byte) ([]byte, error) {
	return encryption.Encrypt(raw, wgtypes.Key(peer), wgtypes.Key(local))
}

func open(peer wg.PublicKey, local wg.PrivateKey, sealed []byte) ([]byte, error) {
	return encryption.Decrypt(sealed, wgtypes.Key(peer), wgtypes.Key(local))
}

func writeFrame(w io.Writer, kind byte, pub wg.PublicKey, sealed []byte) error {
	if len(sealed) > maxFrame {
		return errors.New("frame too large")
	}
	var hdr [headerSize]byte
	hdr[0] = protocolVersion
	hdr[1] = kind
	copy(hdr[2:2+wg.KeySize], pub[:])
	binary.BigEndian.PutUint32(hdr[2+wg.KeySize:], uint32(len(sealed)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(sealed)
	return err
}

func readFrame(r io.Reader) (byte, wg.PublicKey, []byte, error) {
	var hdr [headerSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, wg.PublicKey{}, nil, err
	}
	if hdr[0] != protocolVersion {
		return 0, wg.PublicKey{}, nil, errors.New("bad version")
	}
	var pub wg.PublicKey
	copy(pub[:], hdr[2:2+wg.KeySize])
	n := binary.BigEndian.Uint32(hdr[2+wg.KeySize:])
	if n > maxFrame {
		return 0, wg.PublicKey{}, nil, errors.New("frame too large")
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, wg.PublicKey{}, nil, err
	}
	return hdr[1], pub, body, nil
}

// safePath resolves name inside root and rejects escapes and symlinks.
func safePath(root, name string) (string, error) {
	if name == "" || strings.ContainsRune(name, 0) || filepath.IsAbs(name) {
		return "", errors.New(ErrPath)
	}
	clean := filepath.Clean(name)
	if clean == "." || clean == ".." || clean == inboxName || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New(ErrPath)
	}
	root = filepath.Clean(root)
	full := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, full)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New(ErrPath)
	}
	cur := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		cur = filepath.Join(cur, part)
		st, err := os.Lstat(cur)
		if errors.Is(err, os.ErrNotExist) {
			return full, nil
		}
		if err != nil {
			return "", err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			return "", errors.New(ErrPath)
		}
	}
	return full, nil
}
