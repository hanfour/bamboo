# clients/cli

`bamboo` — the command-line client for the bamboo mesh VPN.

**License:** Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).

## Status

Pre-alpha. The four core subcommands work end-to-end on Linux; other
platforms surface `ErrUnsupported` with a hint to use the platform app.

| Command | What it does |
| --- | --- |
| `bamboo up` | Register with the controller, build a WireGuard config, bring up the interface. Blocks until Ctrl-C. |
| `bamboo down` | Remove the WireGuard interface. Useful if `up` was killed. |
| `bamboo status` | Print interface state via `wgctrl`: peer set, last handshake, transfer counters. |
| `bamboo version` | Print version + commit + build date. |

## Build

```bash
# from repo root
make build
./bin/bamboo --help
```

## Identity

A stable Curve25519 private key is persisted at
`$XDG_CONFIG_HOME/bamboo/private_key` (mode 0600) on first `bamboo up`.
Subsequent runs reuse it, so the controller sees the same peer across
reboots. Delete the file to rotate.

## Walkthroughs

### Path A — tenant-slug fallback (zero-config, dev only)

```bash
sudo ./bin/bamboo up --addr=controller.example.com:8080 --tenant=my-team
# Ctrl-C tears the interface down
```

### Path B — pre-auth key

Mint a key via `grpcurl` (see `clients/core/README.md`) then:

```bash
sudo ./bin/bamboo up --addr=controller.example.com:8080 --auth-key=bka_..._...
```

`--tenant` is ignored when `--auth-key` is set; the tenant comes from
the redeemed key.

### Inspecting state

```bash
sudo ./bin/bamboo status
```

### Manual cleanup

```bash
sudo ./bin/bamboo down
```

## Why root?

Bringing up a WireGuard interface needs `CAP_NET_ADMIN` (manage links,
addresses, routes; talk to the wireguard kernel module). The macOS
PacketTunnelProvider takes the same trust via the system VPN
configuration prompt; that path lives under `clients/macos/`.

## Tracking

- [Sprint 2 — Issue #13](https://github.com/hanfour/bamboo/issues/13) Peer registration handshake
