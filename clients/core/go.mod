module github.com/hanfour/bamboo/clients/core

go 1.25.7

require (
	github.com/hanfour/bamboo/proto v0.0.0-00010101000000-000000000000
	golang.org/x/crypto v0.50.0
	google.golang.org/grpc v1.80.0
)

require (
	go.opentelemetry.io/otel/metric v1.43.0 // indirect
	go.opentelemetry.io/otel/trace v1.43.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260420184626-e10c466a9529 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// In-tree dependency. go.work covers `go build`; this replace lets `go mod
// tidy` resolve the local module without hitting the private GitHub remote.
replace github.com/hanfour/bamboo/proto => ../../proto
