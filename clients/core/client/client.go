// SPDX-License-Identifier: AGPL-3.0-or-later

// Package client wraps the gRPC connection to the controller and exposes
// thin convenience methods over each generated service stub.
package client

import (
	"context"
	"fmt"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// Client holds gRPC service stubs for every controller-side service.
type Client struct {
	conn        *grpc.ClientConn
	Auth        bamboov1.AuthServiceClient
	Coordinator bamboov1.CoordinatorServiceClient
	Policy      bamboov1.PolicyServiceClient
	Telemetry   bamboov1.TelemetryServiceClient
}

// Option tunes Dial.
type Option func(*dialConfig)

type dialConfig struct{ tls bool }

// WithTLS dials over TLS using the system roots. A controller behind a
// reverse proxy (https://host) needs this; the plain gRPC port used in
// local development does not.
func WithTLS() Option { return func(c *dialConfig) { c.tls = true } }

// Dial establishes a connection to a controller at addr. The transport is
// insecure unless WithTLS is given, which is only suitable for a local or
// tunnelled controller.
func Dial(_ context.Context, addr string, opts ...Option) (*Client, error) {
	var cfg dialConfig
	for _, o := range opts {
		o(&cfg)
	}
	creds := insecure.NewCredentials()
	if cfg.tls {
		creds = credentials.NewTLS(nil)
	}
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(creds),
		// A desk file is capped at 4MiB. The protobuf frame around it
		// does not fit in gRPC's default 4MiB message.
		grpc.WithDefaultCallOptions(
			grpc.MaxCallRecvMsgSize(8<<20),
			grpc.MaxCallSendMsgSize(8<<20),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("grpc dial %s: %w", addr, err)
	}

	return &Client{
		conn:        conn,
		Auth:        bamboov1.NewAuthServiceClient(conn),
		Coordinator: bamboov1.NewCoordinatorServiceClient(conn),
		Policy:      bamboov1.NewPolicyServiceClient(conn),
		Telemetry:   bamboov1.NewTelemetryServiceClient(conn),
	}, nil
}

// Close tears down the underlying gRPC connection.
func (c *Client) Close() error {
	return c.conn.Close()
}
