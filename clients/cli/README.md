# clients/cli

`bamboo` — the command-line client for the bamboo mesh VPN.

**License:** source is Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).
The binary links `clients/core`, which is AGPLv3 for now, so a distributed
`bamboo` binary is subject to AGPLv3 terms. See [LICENSING.md](../../LICENSING.md).

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

### Path B — pre-auth key over HTTPS (normal for a deployed controller)

Mint a key in the Web UI (Peers, Add peer) or with `POST /api/v1/preauth-keys`, then:

```bash
sudo env BAMBOO_AUTH_KEY=bka_..._... ./bin/bamboo up --server https://bamboo.example.com
```

`--server` is the controller's public URL. The client uses it for REST and
for gRPC over TLS on the same host and port 443, so the reverse proxy must
route gRPC to the controller (the single-VPS Caddyfile does). Passing the key
through `BAMBOO_AUTH_KEY` keeps it out of the process list. `--tenant` is
ignored when a key is set; the tenant comes from the key.

Every flag below also reads an environment variable when the flag is absent:
`BAMBOO_SERVER`, `BAMBOO_ADDR`, `BAMBOO_TENANT`, `BAMBOO_AUTH_KEY`,
`BAMBOO_HOSTNAME`, `BAMBOO_IFACE`. A flag wins over its variable.

### Path C — run as a service (Linux, systemd)

```bash
sudo env BAMBOO_AUTH_KEY=bka_..._... ./bin/bamboo install-service --server https://bamboo.example.com
journalctl -u bamboo -f
```

This writes `/etc/bamboo/bamboo.env` (mode 0600) and `/etc/systemd/system/bamboo.service`,
then enables and starts it. The device key and the peer session token are kept
in `/var/lib/bamboo`, so a restart re-registers the same peer and does not
redeem a single-use key a second time. If the machine stays off longer than the
session lifetime, the stored key is used again, so give long-lived servers a
reusable key. `bamboo uninstall-service` removes the unit and keeps the key and
environment file.

### Path D — local controller without TLS

```bash
sudo ./bin/bamboo up --addr=localhost:8080 --auth-key=bka_..._...
```

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
addresses, routes; talk to the wireguard kernel module). The macOS /
iOS PacketTunnelProvider takes the same trust via the system VPN
configuration prompt; that path lives under `clients/apple/`.

## Tracking

- [Sprint 2 — Issue #13](https://github.com/hanfour/bamboo/issues/13) Peer registration handshake
