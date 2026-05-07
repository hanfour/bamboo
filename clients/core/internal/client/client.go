// SPDX-License-Identifier: AGPL-3.0-or-later

// Package client wraps the gRPC connection to the controller and exposes
// thin convenience methods over each generated service stub.
package client

import (
	"context"
	"fmt"

	bamboov1 "github.com/hanfour/bamboo/proto/gen/go/bamboo/v1"
	"google.golang.org/grpc"
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

// Dial establishes a connection to a controller at addr.
//
// The current implementation uses insecure transport for local development.
// Production deployments must add TLS via grpc.WithTransportCredentials.
func Dial(_ context.Context, addr string) (*Client, error) {
	conn, err := grpc.NewClient(
		addr,
		grpc.WithTransportCredentials(insecure.NewCredentials()),
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
