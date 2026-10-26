// SPDX-License-Identifier: AGPL-3.0-or-later

package handlers

import (
	"context"
	"errors"
	"io"

	"github.com/google/uuid"
	"github.com/hanfour/bamboo/apps/controller/internal/auth"
	"github.com/hanfour/bamboo/apps/controller/internal/db/repo"
	"github.com/hanfour/bamboo/apps/controller/internal/operate"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// operateStream is the subset of the generated bidi stream the desk uses.
type operateStream interface {
	Context() context.Context
	Send(*bamboov1.AdminOperateFrame) error
	Recv() (*bamboov1.DeviceOperateFrame, error)
}

// Operate accepts the device's desk stream. The first frame must be hello.
func (h *CoordinatorHandler) Operate(stream grpc.BidiStreamingServer[bamboov1.DeviceOperateFrame, bamboov1.AdminOperateFrame]) error {
	return h.serveOperate(stream)
}

func (h *CoordinatorHandler) serveOperate(stream operateStream) error {
	if h == nil || h.ops == nil {
		return status.Error(codes.FailedPrecondition, "operate hub unavailable")
	}
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	hello := first.GetHello()
	if hello == nil || hello.GetPeerId() == "" {
		return status.Error(codes.InvalidArgument, "operate hello is required")
	}
	ctx := stream.Context()
	if err := h.requireDeviceSession(ctx, hello.GetPeerId()); err != nil {
		return err
	}
	if _, err := h.lookupOperatePeer(ctx, hello.GetPeerId()); err != nil {
		return err
	}
	frames, done, detach := h.ops.Attach(hello.GetPeerId(), hello.GetExecEnabled())
	defer detach()

	errCh := make(chan error, 1)
	go func() {
		for {
			frame, rerr := stream.Recv()
			if rerr != nil {
				errCh <- rerr
				return
			}
			h.applyDeviceFrame(hello.GetPeerId(), frame)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-done:
			return nil
		case err := <-errCh:
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		case frame, ok := <-frames:
			if !ok {
				return nil
			}
			msg, ok := adminProto(frame)
			if !ok {
				continue
			}
			if err := stream.Send(msg); err != nil {
				return err
			}
		}
	}
}

// requireDeviceSession admits only the device itself to the desk
// stream. Admins send files and terminal keystrokes down this stream
// and trust what comes back, so the tenant-level check that
// enforcePeerBinding applies to a user-session JWT is not enough here:
// any member of the tenant could attach as another user's machine and
// take over its link. When the controller has a session secret, the
// bearer must be a peer-session token minted for this exact peer.
// Without a secret (dev mode) there is nothing to verify against.
func (h *CoordinatorHandler) requireDeviceSession(ctx context.Context, peerID string) error {
	if h.auth == nil || len(h.auth.sessionSec) == 0 {
		return nil
	}
	token := bearerFromMetadata(ctx)
	if token == "" {
		return status.Error(codes.Unauthenticated, "operate requires the device's peer-session token")
	}
	claims, err := auth.VerifyPeerSessionToken(h.auth.sessionSec, token)
	if err != nil {
		return status.Error(codes.PermissionDenied, "operate requires the device's peer-session token")
	}
	if claims.PeerID.String() != peerID {
		return status.Error(codes.PermissionDenied, "peer-session token is not bound to the requested peer")
	}
	return nil
}

func (h *CoordinatorHandler) applyDeviceFrame(peerID string, frame *bamboov1.DeviceOperateFrame) {
	if h == nil || h.ops == nil || frame == nil {
		return
	}
	if res := frame.GetResult(); res != nil {
		h.ops.Result(peerID, operate.Result{
			OpID:     res.GetOpId(),
			OK:       res.GetOk(),
			Error:    res.GetError(),
			ExitCode: int(res.GetExitCode()),
			Data:     append([]byte(nil), res.GetData()...),
		})
		return
	}
	if out := frame.GetOutput(); out != nil {
		h.ops.Output(peerID, out.GetOpId(), out.GetData())
	}
}

func adminProto(f operate.Frame) (*bamboov1.AdminOperateFrame, bool) {
	switch f.Kind {
	case operate.KindMsg:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_Msg{Msg: &bamboov1.AdminOperateMsg{
			OpId: f.OpID, From: f.From, Text: f.Text,
		}}}, true
	case operate.KindPut:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_Put{Put: &bamboov1.AdminOperatePut{
			OpId: f.OpID, Path: f.Path, Data: f.Data,
		}}}, true
	case operate.KindGet:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_Get{Get: &bamboov1.AdminOperateGet{
			OpId: f.OpID, Path: f.Path,
		}}}, true
	case operate.KindPTYStart:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_PtyStart{PtyStart: &bamboov1.AdminOperatePTYStart{
			OpId: f.OpID, Argv: f.Argv, Rows: int32(f.Rows), Cols: int32(f.Cols),
		}}}, true
	case operate.KindPTYData:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_PtyIn{PtyIn: &bamboov1.AdminOperatePTYIn{
			OpId: f.OpID, Data: f.Data,
		}}}, true
	case operate.KindPTYResize:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_PtyWinsize{PtyWinsize: &bamboov1.AdminOperatePTYWinsize{
			OpId: f.OpID, Rows: int32(f.Rows), Cols: int32(f.Cols),
		}}}, true
	case operate.KindPTYClose:
		return &bamboov1.AdminOperateFrame{Frame: &bamboov1.AdminOperateFrame_PtyClose{PtyClose: &bamboov1.AdminOperatePTYClose{
			OpId: f.OpID,
		}}}, true
	default:
		return nil, false
	}
}

// DeskCall checks the peer is in the caller's tenant, then waits for
// the device. The tenant transaction ends before the wait.
func (h *CoordinatorHandler) DeskCall(ctx context.Context, peerID uuid.UUID, f operate.Frame) (operate.Result, error) {
	if err := h.requireDesk(ctx, peerID); err != nil {
		return operate.Result{}, err
	}
	return h.ops.Call(ctx, peerID.String(), f)
}

// DeskPush sends terminal input that does not wait for a result.
func (h *CoordinatorHandler) DeskPush(ctx context.Context, peerID uuid.UUID, f operate.Frame) error {
	if err := h.requireDesk(ctx, peerID); err != nil {
		return err
	}
	return h.ops.Push(ctx, peerID.String(), f)
}

// DeskStatus reports whether the device's stream is up and whether it
// enabled a terminal.
func (h *CoordinatorHandler) DeskStatus(ctx context.Context, peerID uuid.UUID) (online, exec bool, err error) {
	if err := h.requireDesk(ctx, peerID); err != nil {
		return false, false, err
	}
	exec, online = h.ops.ExecEnabled(peerID.String())
	return online, exec, nil
}

// DeskSubscribe returns the device's current terminal. The caller must
// already have loaded the peer; this checks tenancy again and does not
// hold the transaction while bytes flow.
func (h *CoordinatorHandler) DeskSubscribe(ctx context.Context, peerID uuid.UUID) (operate.PTYSub, error) {
	if err := h.requireDesk(ctx, peerID); err != nil {
		return operate.PTYSub{}, err
	}
	sub, ok := h.ops.Subscribe(peerID.String())
	if !ok {
		if _, online := h.ops.ExecEnabled(peerID.String()); !online {
			return operate.PTYSub{}, operate.ErrOffline
		}
		return operate.PTYSub{}, operate.ErrNoSession
	}
	return sub, nil
}

// OnDeskSessionEnd calls fn on its own goroutine each time a terminal
// session on a device ends. It carries sizes and timing, never contents.
func (h *CoordinatorHandler) OnDeskSessionEnd(fn func(peerID uuid.UUID, e operate.PTYEnd)) {
	if h == nil || h.ops == nil || fn == nil {
		return
	}
	h.ops.SetOnPTYEnd(func(peerID string, e operate.PTYEnd) {
		id, err := uuid.Parse(peerID)
		if err != nil {
			return
		}
		fn(id, e)
	})
}

func (h *CoordinatorHandler) requireDesk(ctx context.Context, peerID uuid.UUID) error {
	if h == nil || h.ops == nil {
		return status.Error(codes.FailedPrecondition, "operate hub unavailable")
	}
	if _, err := h.lookupOperatePeer(ctx, peerID.String()); err != nil {
		return err
	}
	return nil
}

func (h *CoordinatorHandler) lookupOperatePeer(ctx context.Context, peerIDStr string) (*repo.Peer, error) {
	if peerIDStr == "" {
		return nil, status.Error(codes.InvalidArgument, "peer_id is required")
	}
	peerID, err := uuid.Parse(peerIDStr)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid peer_id: %v", err)
	}
	if h.pool == nil {
		return &repo.Peer{ID: peerID}, nil
	}
	if tid, ok := resolvedTenantID(ctx, h.auth); ok {
		ctx = WithKnownTenantID(ctx, tid)
	}
	peer, err := h.loadPeerForMutation(ctx, peerID)
	if err != nil {
		if errors.Is(err, repo.ErrNotFound) {
			return nil, status.Error(codes.NotFound, "peer not found")
		}
		return nil, status.Errorf(codes.Internal, "get peer: %v", err)
	}
	return peer, nil
}
