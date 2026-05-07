// SPDX-License-Identifier: AGPL-3.0-or-later

// Command dev-agent is a tiny development binary used to validate the
// gRPC contract between client and controller. It is not the production
// agent; that lives in clients/cli (and later in the platform-specific
// directories).
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/hanfour/bamboo/clients/core/internal/client"
	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
)

func main() {
	addr := flag.String("addr", "localhost:8080", "controller gRPC address")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cli, err := client.Dial(ctx, *addr)
	if err != nil {
		fail("dial: %v", err)
	}
	defer cli.Close()

	slog.Info("connected to controller", "addr", *addr)

	resp, err := cli.Coordinator.Register(ctx, &bamboov1.RegisterRequest{
		Hostname:      "dev-agent",
		Os:            "darwin",
		ClientVersion: "dev",
	})
	if err != nil {
		// During scaffolding the controller stub returns Unimplemented;
		// this confirms wiring is correct end-to-end.
		slog.Info("register returned error (expected while handlers are stubs)",
			"err", err.Error())
		return
	}
	fmt.Printf("register OK: peer=%s ip=%s peers_in_set=%d\n",
		resp.GetSelf().GetId(), resp.GetSelf().GetIp(), len(resp.GetPeers()))
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
