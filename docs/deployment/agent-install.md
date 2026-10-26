# Install and sign in with a coding agent

A coding agent (Claude Code, Codex, Grok and similar) can enroll a Linux
machine in one command. Signing in uses a pre-auth key, not a Google login:
the agent cannot complete a browser sign-in, and should never be handed your
account password. A person creates the key, the agent redeems it.

## 1. Create the key (you)

Web UI, Peers, Add peer. Choose:

| Situation | Reusable | Auto-approve | Tags |
| --- | --- | --- | --- |
| One agent-driven install | off (single-use) | on | a tag that limits what the device can reach, such as `agent` |
| A server that restarts and may be offline for days | on | on | the server's role tag |

A single-use key is spent by the first register. The client then keeps a
peer session token in `/var/lib/bamboo`, so restarts re-register the same
device without the key. If the device is offline longer than that token lives,
only a reusable key lets it come back.

The dialog shows the command and a prompt to paste. The key appears once.

## 2. Run it (the agent, or you)

```bash
curl -fsSL https://bamboo.example.com/install.sh | sudo env BAMBOO_AUTH_KEY='bka_...' sh
```

The script downloads `bamboo-linux-<arch>` from the GitHub release, checks it
against `SHA256SUMS`, installs it to `/usr/local/bin/bamboo`, and runs
`bamboo install-service`. That writes `/etc/bamboo/bamboo.env` (mode 0600) and
a systemd unit, then starts it. Variables:

| Variable | Meaning |
| --- | --- |
| `BAMBOO_AUTH_KEY` | the pre-auth key; without it the binary is installed but not registered |
| `BAMBOO_HOSTNAME` | peer name, default the machine's hostname |
| `BAMBOO_SERVER` | controller URL, default the server the script came from |
| `BAMBOO_VERSION` | a release tag such as `v0.1.17`, default latest |
| `BAMBOO_NO_SERVICE=1` | install the binary only |

## 3. Check it

```bash
systemctl status bamboo --no-pager
sudo bamboo status
journalctl -u bamboo -n 50 --no-pager
```

The machine appears under Peers in the Web UI within a few seconds.

## Prompt for an agent

> Install the bamboo client on this Linux machine with the command I give you,
> then run `systemctl status bamboo --no-pager` and `sudo bamboo status` and
> report the mesh IP. Do not print the key again, write it to a file or commit
> it. If the command fails after registering, do not retry; tell me.

## Remote shell is off, and `--session-exec` means root

The unit that `install-service` writes runs `bamboo up` without
`--session-exec`. In that mode the device accepts messages and file
transfers from the desk, and refuses to start a terminal.

Do not add `--session-exec` unless you want an admin of this tenant to have a
shell on the machine. The service runs as root, so that shell is a **root
shell**. Terminal start and close are audited (`peer.desk.term`,
`peer.desk.term.close`); the commands and output are not recorded.

The unit sets `NoNewPrivileges=true`, so programs started from that shell
cannot gain more privileges through setuid binaries such as `sudo`. That is a
small extra limit, not a sandbox: the shell is already root.

## What can go wrong

| Symptom | Cause and fix |
| --- | --- |
| `checksum mismatch` | The download was corrupted or tampered with. Do not install; retry, and check the release page. |
| `download failed` | The controller's repository has no release yet. A release is created when a `v*` tag is pushed. |
| `register http` with `pre-auth key already used`, `not found`, `revoked`, `expired` or `invalid pre-auth key format` | The key was mistyped, revoked, expired or already spent. Create a new one. |
| `dial controller` or `Unavailable` | The proxy does not route gRPC. On the single-VPS stack, check that the Caddyfile has the `/bamboo.v1.*` handler and reload Caddy. |
| `wireguard kernel module is not available` | Use Linux 5.6 or newer, or install the WireGuard package. |
| Works as root but not in a container | The container needs `CAP_NET_ADMIN` and `/dev/net/tun`. |

## For people writing agent instructions

- Keep the key out of files, shell history and logs. The installer passes it
  through the environment on purpose.
- Use a tag on the key so the device gets only the access its job needs. See
  [onboarding](onboarding.md) for how tags map to access rules.
- macOS and iOS are not covered here. They need the signed app.
