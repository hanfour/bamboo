module github.com/hanfour/bamboo/clients/core

go 1.23

require (
	github.com/hanfour/bamboo/proto v0.0.0-00010101000000-000000000000
	google.golang.org/grpc v1.68.0
)

require (
	golang.org/x/net v0.29.0 // indirect
	golang.org/x/sys v0.25.0 // indirect
	golang.org/x/text v0.18.0 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20240903143218-8af14fe29dc1 // indirect
	google.golang.org/protobuf v1.35.1 // indirect
)

// In-tree dependency. go.work covers `go build`; this replace lets `go mod
// tidy` resolve the local module without hitting the private GitHub remote.
replace github.com/hanfour/bamboo/proto => ../../proto
