module github.com/hanfour/bamboo/clients/cli

go 1.25.7

require (
	github.com/hanfour/bamboo/clients/core v0.0.0-00010101000000-000000000000
	github.com/hanfour/bamboo/proto v0.0.0-00010101000000-000000000000
	github.com/spf13/cobra v1.8.1
	golang.zx2c4.com/wireguard/wgctrl v0.0.0-20241231184526-a9ab2273dd10
	google.golang.org/grpc v1.80.0
)

require (
	github.com/google/go-cmp v0.7.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/josharian/native v1.1.0 // indirect
	github.com/mdlayher/genetlink v1.3.2 // indirect
	github.com/mdlayher/netlink v1.7.2 // indirect
	github.com/mdlayher/socket v0.5.1 // indirect
	github.com/spf13/pflag v1.0.5 // indirect
	github.com/vishvananda/netlink v1.3.1 // indirect
	github.com/vishvananda/netns v0.0.5 // indirect
	golang.org/x/crypto v0.50.0 // indirect
	golang.org/x/net v0.53.0 // indirect
	golang.org/x/sync v0.20.0 // indirect
	golang.org/x/sys v0.43.0 // indirect
	golang.org/x/text v0.36.0 // indirect
	golang.zx2c4.com/wireguard v0.0.0-20231211153847-12269c276173 // indirect
	google.golang.org/genproto/googleapis/rpc v0.0.0-20260420184626-e10c466a9529 // indirect
	google.golang.org/protobuf v1.36.11 // indirect
)

// In-tree dependencies. go.work covers `go build`; these replaces let
// `go mod tidy` resolve local modules without hitting the GitHub remote.
replace (
	github.com/hanfour/bamboo/clients/core => ../core
	github.com/hanfour/bamboo/proto => ../../proto
)
