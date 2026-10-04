// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hanfour/bamboo/clients/cli/internal/state"
	clientsync "github.com/hanfour/bamboo/clients/cli/internal/sync"
	"github.com/hanfour/bamboo/clients/core/session"
	"github.com/hanfour/bamboo/clients/core/wg"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"github.com/spf13/cobra"
	"golang.org/x/term"
	"google.golang.org/grpc/metadata"
)

const sessionCallTimeout = 30 * time.Second

var (
	flagSessionListen string
	flagSessionAllow  []string
	flagSessionRoot   string
	flagSessionExec   bool
	flagPeerKey       string
	flagInboxRoot     string
)

var sessionCmd = &cobra.Command{
	Use:   "session",
	Short: "Device-to-device session listener",
}

var sessionServeCmd = &cobra.Command{
	Use:   "serve",
	Short: "Accept terminal, exec, file, and message requests from allowlisted peers",
	Long: `Listen for session requests. A peer must present a WireGuard key
listed in --allow. With no --allow entries every handshake is denied.

--enable-exec lets an allowed peer run a command as argv. The command
is not passed through a shell. File transfer stays inside the session
root. Messages append to inbox.jsonl in that root.

Bind --listen to this device's mesh IP when the tunnel is up so only
peers that can route here can connect. AllowedIPs stay the network gate.`,
	RunE: runSessionServe,
}

var execCmd = &cobra.Command{
	Use:   "exec <host:port> -- <command> [args...]",
	Short: "Run a command on one peer",
	Args:  cobra.MinimumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), sessionCallTimeout)
		defer cancel()
		resp, err := sessionCall(ctx, args[0], session.Request{Op: session.OpExec, Argv: args[1:]})
		if resp.Stdout != nil {
			_, _ = cmd.OutOrStdout().Write(resp.Stdout)
		}
		if resp.Stderr != nil {
			_, _ = cmd.ErrOrStderr().Write(resp.Stderr)
		}
		if err != nil {
			return err
		}
		if resp.ExitCode != 0 {
			return fmt.Errorf("remote exit %d", resp.ExitCode)
		}
		return nil
	},
}

var termCmd = &cobra.Command{
	Use:   "term <host:port>",
	Short: "Open an interactive terminal on a peer",
	Long: `Attach to a shell on the peer. The peer must be listening with
exec enabled (bamboo session serve --enable-exec, or bamboo up
--session-exec). When stdin is a terminal it is switched to raw mode
and window-size changes are forwarded.`,
	Args: cobra.ExactArgs(1),
	RunE: runTerm,
}

var fileCmd = &cobra.Command{
	Use:   "file",
	Short: "Copy a file to or from a peer's session root",
}

var filePutCmd = &cobra.Command{
	Use:   "put <host:port> <local-path> <remote-path>",
	Short: "Copy a local file into a peer's session root",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		data, err := readLocalFile(args[1])
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), sessionCallTimeout)
		defer cancel()
		_, err = sessionCall(ctx, args[0], session.Request{
			Op:   session.OpPut,
			Path: args[2],
			Data: data,
		})
		return err
	},
}

var fileGetCmd = &cobra.Command{
	Use:   "get <host:port> <remote-path> <local-path>",
	Short: "Copy a file out of a peer's session root",
	Args:  cobra.ExactArgs(3),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), sessionCallTimeout)
		defer cancel()
		resp, err := sessionCall(ctx, args[0], session.Request{
			Op:   session.OpGet,
			Path: args[1],
		})
		if err != nil {
			return err
		}
		return writeLocalFile(args[2], resp.Data)
	},
}

var msgCmd = &cobra.Command{
	Use:   "msg",
	Short: "Send or read a short device message",
}

var msgSendCmd = &cobra.Command{
	Use:   "send <host:port> <text>",
	Short: "Append a short message to a peer's inbox",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		ctx, cancel := context.WithTimeout(cmd.Context(), sessionCallTimeout)
		defer cancel()
		_, err := sessionCall(ctx, args[0], session.Request{Op: session.OpMsg, Text: args[1]})
		return err
	},
}

var msgInboxCmd = &cobra.Command{
	Use:   "inbox",
	Short: "Print this device's session inbox",
	RunE: func(cmd *cobra.Command, _ []string) error {
		root, err := sessionRoot(flagInboxRoot)
		if err != nil {
			return err
		}
		entries, err := session.ReadInbox(root)
		if err != nil {
			return err
		}
		for _, ent := range entries {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\t%s\t%s\n", ent.At, ent.From, ent.Text); err != nil {
				return err
			}
		}
		return nil
	},
}

func init() {
	sessionServeCmd.Flags().StringVar(&flagSessionListen, "listen", fmt.Sprintf(":%d", session.DefaultPort), "TCP address to listen on")
	sessionServeCmd.Flags().StringSliceVar(&flagSessionAllow, "allow", nil, "WireGuard public keys allowed to open a session (repeatable)")
	sessionServeCmd.Flags().StringVar(&flagSessionRoot, "root", "", "directory for transferred files and the inbox (default: config dir/session)")
	sessionServeCmd.Flags().BoolVar(&flagSessionExec, "enable-exec", false, "allow an allowlisted peer to run a command")
	msgInboxCmd.Flags().StringVar(&flagInboxRoot, "root", "", "session root to read (default: config dir/session)")

	for _, c := range []*cobra.Command{execCmd, termCmd, filePutCmd, fileGetCmd, msgSendCmd} {
		c.Flags().StringVar(&flagPeerKey, "peer-key", "", "remote peer WireGuard public key (base64)")
	}

	sessionCmd.AddCommand(sessionServeCmd)
	fileCmd.AddCommand(filePutCmd, fileGetCmd)
	msgCmd.AddCommand(msgSendCmd, msgInboxCmd)
	rootCmd.AddCommand(sessionCmd, execCmd, termCmd, fileCmd, msgCmd)
}

// peerGate allows whatever public keys are currently in the controller
// peer cache. Join and leave take effect on the next handshake.
type peerGate struct {
	cache *clientsync.PeerCache
}

func (g peerGate) Allows(k wg.PublicKey) bool {
	if g.cache == nil {
		return false
	}
	_, peers := g.cache.Snapshot()
	want := k.Base64()
	for _, p := range peers {
		if p.GetWireguardPublicKey() == want {
			return true
		}
	}
	return false
}

func sessionListenAddr(ip string) (string, error) {
	addr, err := netip.ParseAddr(strings.TrimSpace(ip))
	if err != nil {
		return "", fmt.Errorf("session listen ip %q: %w", ip, err)
	}
	if !addr.IsValid() {
		return "", fmt.Errorf("session listen ip %q", ip)
	}
	return net.JoinHostPort(addr.String(), strconv.Itoa(session.DefaultPort)), nil
}

// startOperateDesk opens the admin desk stream. The controller reaches
// this device through that stream; a failure here does not take the
// tunnel down.
func startOperateDesk(ctx context.Context, coord bamboov1.CoordinatorServiceClient, sess *peerSession, peerID string) {
	if coord == nil || sess == nil || peerID == "" {
		return
	}
	root, err := sessionRoot("")
	if err != nil {
		slog.Warn("operate desk", "err", err)
		return
	}
	desk, err := session.OpenDesk(root, flagUpSessionExec)
	if err != nil {
		slog.Warn("operate desk", "err", err)
		return
	}
	go clientsync.RunOperate(ctx, func(callCtx context.Context) (clientsync.OperateStream, error) {
		if tok := sess.Token(); tok != "" {
			callCtx = metadata.AppendToOutgoingContext(callCtx, "authorization", "Bearer "+tok)
		}
		return coord.Operate(callCtx)
	}, peerID, flagUpSessionExec, desk)
}

// startMeshSession listens on the device's mesh IP. A bind failure is
// logged and does not take the tunnel down.
func startMeshSession(ctx context.Context, priv wg.PrivateKey, meshIP string, cache *clientsync.PeerCache) {
	addr, err := sessionListenAddr(meshIP)
	if err != nil {
		slog.Warn("session listen", "err", err)
		return
	}
	root, err := sessionRoot("")
	if err != nil {
		slog.Warn("session root", "err", err)
		return
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		slog.Warn("session listen", "addr", addr, "err", err)
		return
	}
	slog.Info("session listening", "addr", ln.Addr().String(), "exec", flagUpSessionExec)
	go func() {
		err := session.Serve(ctx, ln, session.Config{
			PrivateKey: priv,
			Check:      peerGate{cache: cache},
			Root:       root,
			EnableExec: flagUpSessionExec,
		})
		if err != nil && ctx.Err() == nil {
			slog.Warn("session ended", "err", err)
		}
	}()
}

func runSessionServe(cmd *cobra.Command, _ []string) error {
	priv, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		return err
	}
	allow := make([]wg.PublicKey, 0, len(flagSessionAllow))
	for _, raw := range flagSessionAllow {
		pub, err := wg.PublicKeyFromBase64(strings.TrimSpace(raw))
		if err != nil {
			return fmt.Errorf("allow %q: %w", raw, err)
		}
		allow = append(allow, pub)
	}
	root, err := sessionRoot(flagSessionRoot)
	if err != nil {
		return err
	}
	ln, err := net.Listen("tcp", flagSessionListen)
	if err != nil {
		return err
	}
	defer func() { _ = ln.Close() }()
	_, _ = fmt.Fprintf(os.Stderr, "session listening on %s root %s exec %v\n", ln.Addr(), root, flagSessionExec)
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return session.Serve(ctx, ln, session.Config{
		PrivateKey: priv,
		Allow:      allow,
		Root:       root,
		EnableExec: flagSessionExec,
	})
}

func runTerm(cmd *cobra.Command, args []string) error {
	if strings.TrimSpace(flagPeerKey) == "" {
		return fmt.Errorf("--peer-key is required")
	}
	priv, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		return err
	}
	peer, err := wg.PublicKeyFromBase64(strings.TrimSpace(flagPeerKey))
	if err != nil {
		return fmt.Errorf("--peer-key: %w", err)
	}
	parent := cmd.Context()
	if parent == nil {
		parent = context.Background()
	}
	rows, cols := 24, 80
	var tty *os.File
	if f, ok := cmd.InOrStdin().(*os.File); ok && term.IsTerminal(int(f.Fd())) {
		tty = f
		if r, c, err := term.GetSize(int(f.Fd())); err == nil && r > 0 && c > 0 {
			rows, cols = r, c
		}
		raw, err := term.MakeRaw(int(f.Fd()))
		if err != nil {
			return err
		}
		defer func() { _ = term.Restore(int(f.Fd()), raw) }()
	}
	hctx, cancel := context.WithTimeout(parent, sessionCallTimeout)
	remote, err := session.DialTerm(hctx, args[0], priv, peer, nil, rows, cols)
	cancel()
	if err != nil {
		return err
	}
	defer func() { _ = remote.Close() }()
	if tty != nil {
		winch := make(chan os.Signal, 1)
		signal.Notify(winch, syscall.SIGWINCH)
		defer signal.Stop(winch)
		go func() {
			for {
				select {
				case <-parent.Done():
					return
				case <-winch:
					r, c, err := term.GetSize(int(tty.Fd()))
					if err == nil {
						_ = remote.Resize(r, c)
					}
				}
			}
		}()
	}
	go func() {
		<-parent.Done()
		_ = remote.Close()
	}()
	stdoutDone := make(chan error, 1)
	go func() {
		_, err := io.Copy(cmd.OutOrStdout(), remote)
		stdoutDone <- err
	}()
	go func() {
		_, err := io.Copy(remote, cmd.InOrStdin())
		if err == nil {
			_, _ = remote.Write([]byte{0x04})
		}
	}()
	code, exitErr := remote.ExitCode()
	copyErr := <-stdoutDone
	if parent.Err() != nil {
		return parent.Err()
	}
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return copyErr
	}
	if exitErr != nil && !errors.Is(exitErr, io.EOF) {
		return exitErr
	}
	if code != 0 {
		return fmt.Errorf("remote exit %d", code)
	}
	return nil
}

func sessionCall(ctx context.Context, addr string, req session.Request) (session.Response, error) {
	var zero session.Response
	if strings.TrimSpace(flagPeerKey) == "" {
		return zero, fmt.Errorf("--peer-key is required")
	}
	priv, err := state.LoadOrCreatePrivateKey()
	if err != nil {
		return zero, err
	}
	peer, err := wg.PublicKeyFromBase64(strings.TrimSpace(flagPeerKey))
	if err != nil {
		return zero, fmt.Errorf("--peer-key: %w", err)
	}
	return session.Call(ctx, addr, priv, peer, req)
}

func sessionRoot(flag string) (string, error) {
	if flag != "" {
		if err := os.MkdirAll(flag, 0o700); err != nil {
			return "", err
		}
		return flag, nil
	}
	dir, err := state.Dir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(dir, "session")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", err
	}
	return root, nil
}

func readLocalFile(path string) ([]byte, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if st.Size() > 4<<20 {
		return nil, fmt.Errorf("%s is larger than 4MiB", path)
	}
	return os.ReadFile(path) //nolint:gosec // G304: local path the operator named on the command line
}

func writeLocalFile(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600) //nolint:gosec // G306: mode 0600; path the operator named
}
