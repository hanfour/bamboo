# clients/linux

Linux daemon + CLI.

**License:** Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).

## Status

Packaging notes only. The Linux agent is the `bamboo` CLI in
[`clients/cli`](../cli), which brings up WireGuard through `clients/core`.
DEB / RPM packaging and a systemd unit are not built yet.

## Stack

- Go 1.23+
- systemd unit
- WireGuard kernel module (preferred) or `wireguard-go` userspace fallback
- DEB / RPM packages
