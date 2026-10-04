// SPDX-License-Identifier: Apache-2.0

package sync

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/hanfour/bamboo/clients/core/session"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// OperateStream is the device half of CoordinatorService.Operate.
type OperateStream interface {
	Send(*bamboov1.DeviceOperateFrame) error
	Recv() (*bamboov1.AdminOperateFrame, error)
}

// OperateDialer opens one Operate stream. The CLI passes a dialer that
// attaches the peer-session bearer.
type OperateDialer func(ctx context.Context) (OperateStream, error)

// operateAfter is the reconnect wait. Tests replace it.
var operateAfter = time.After

// RunOperate keeps the admin desk connected. An old controller that
// does not know Operate is retried with backoff; the tunnel stays up.
func RunOperate(ctx context.Context, dial OperateDialer, peerID string, exec bool, desk *session.Desk) {
	if ctx == nil || dial == nil || desk == nil || peerID == "" {
		return
	}
	backoff := initialBackoff
	warnedUnsupported := false
	for ctx.Err() == nil {
		err := operateOnce(ctx, dial, peerID, exec, desk)
		if ctx.Err() != nil {
			return
		}
		if status.Code(err) == codes.Unimplemented {
			if !warnedUnsupported {
				slog.Warn("controller has no device desk; retrying", "err", err)
				warnedUnsupported = true
			} else {
				slog.Debug("controller has no device desk", "err", err)
			}
		} else {
			warnedUnsupported = false
			slog.Warn("device desk disconnected", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-operateAfter(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

func operateOnce(ctx context.Context, dial OperateDialer, peerID string, exec bool, desk *session.Desk) error {
	stream, err := dial(ctx)
	if err != nil {
		return err
	}
	if stream == nil {
		return errors.New("operate dial returned no stream")
	}
	opCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer func() {
		desk.Handle(session.DeskOp{Kind: session.DeskPTYClose})
	}()
	if err := stream.Send(&bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Hello{Hello: &bamboov1.DeviceOperateHello{
		PeerId:      peerID,
		ExecEnabled: exec,
	}}}); err != nil {
		return err
	}

	var sendMu sync.Mutex
	send := func(frame *bamboov1.DeviceOperateFrame) error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return stream.Send(frame)
	}
	var pumpMu sync.Mutex
	var pumpCancel context.CancelFunc
	defer func() {
		pumpMu.Lock()
		if pumpCancel != nil {
			pumpCancel()
		}
		pumpMu.Unlock()
	}()

	for {
		frame, err := stream.Recv()
		if err != nil {
			return err
		}
		if err := handleAdminFrame(opCtx, desk, frame, send, &pumpMu, &pumpCancel); err != nil {
			return err
		}
	}
}

func handleAdminFrame(ctx context.Context, desk *session.Desk, frame *bamboov1.AdminOperateFrame, send func(*bamboov1.DeviceOperateFrame) error, pumpMu *sync.Mutex, pumpCancel *context.CancelFunc) error {
	opID, op, ok := adminToDesk(frame)
	if !ok {
		return nil
	}
	reply, term := desk.Handle(op)
	switch op.Kind {
	case session.DeskMsg, session.DeskPut, session.DeskGet, session.DeskPTYStart:
		if err := send(deskResult(opID, reply)); err != nil {
			return err
		}
	}
	if term != nil {
		startDeskPump(ctx, opID, term, send, pumpMu, pumpCancel)
	}
	return nil
}

func startDeskPump(parent context.Context, opID string, term *session.TermBytes, send func(*bamboov1.DeviceOperateFrame) error, pumpMu *sync.Mutex, pumpCancel *context.CancelFunc) {
	pumpMu.Lock()
	if *pumpCancel != nil {
		(*pumpCancel)()
	}
	ctx, cancel := context.WithCancel(parent)
	*pumpCancel = cancel
	pumpMu.Unlock()
	go func() {
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				return
			case chunk, ok := <-term.Out:
				if !ok {
					code := 0
					if term.Code != nil {
						code = term.Code()
					}
					_ = send(deskResult(opID, session.DeskReply{OK: true, ExitCode: code}))
					return
				}
				if err := send(&bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Output{Output: &bamboov1.DeviceOperateOutput{
					OpId: opID,
					Data: chunk,
				}}}); err != nil {
					return
				}
			}
		}
	}()
}

func adminToDesk(frame *bamboov1.AdminOperateFrame) (string, session.DeskOp, bool) {
	if frame == nil {
		return "", session.DeskOp{}, false
	}
	if m := frame.GetMsg(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskMsg, From: m.GetFrom(), Text: m.GetText()}, true
	}
	if m := frame.GetPut(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskPut, Path: m.GetPath(), Data: m.GetData()}, true
	}
	if m := frame.GetGet(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskGet, Path: m.GetPath()}, true
	}
	if m := frame.GetPtyStart(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskPTYStart, Argv: m.GetArgv(), Rows: int(m.GetRows()), Cols: int(m.GetCols())}, true
	}
	if m := frame.GetPtyIn(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskPTYData, Data: m.GetData()}, true
	}
	if m := frame.GetPtyWinsize(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskPTYResize, Rows: int(m.GetRows()), Cols: int(m.GetCols())}, true
	}
	if m := frame.GetPtyClose(); m != nil {
		return m.GetOpId(), session.DeskOp{Kind: session.DeskPTYClose}, true
	}
	return "", session.DeskOp{}, false
}

func deskResult(opID string, reply session.DeskReply) *bamboov1.DeviceOperateFrame {
	return &bamboov1.DeviceOperateFrame{Frame: &bamboov1.DeviceOperateFrame_Result{Result: &bamboov1.DeviceOperateResult{
		OpId:     opID,
		Ok:       reply.OK,
		Error:    reply.Error,
		ExitCode: int32(reply.ExitCode),
		Data:     reply.Data,
	}}}
}
