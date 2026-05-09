# Deploying a bamboo relay on a $5 VPS

A bamboo relay is a small DERP-style WebSocket forwarder that
proxies WireGuard packets between peers stuck behind symmetric NAT
or restrictive egress firewalls. It does not see plaintext: the
relay only sees encrypted WG payloads + a 32-byte destination key.
See [ADR 0013](../adr/0013-relay-protocol.md) for the protocol design.

This walkthrough takes you from "I have a fresh VPS" to "my Mac and
iPhone can fall back to relay over cellular" in about 15 minutes.

## What you need

- A VPS with a public IPv4 address. Cloudflare, Vultr, Hetzner, or
  AWS Lightsail all work; $5/mo is plenty for a personal-scale
  relay.
- A DNS name pointing at that VPS (e.g. `relay.yourdomain.com`).
  Cloudflare DNS is fine — turn the orange cloud OFF for this
  record so Let's Encrypt can complete the HTTP-01 challenge.
- Docker installed on the VPS.
- Your bamboo controller's `session_secret` (the same value used in
  `apps/controller/config/example.yaml` under `auth.session_secret`).
  The relay verifies controller-issued JWTs with this secret, so it
  must match.

## Steps

### 1. Build + publish the relay image (once per release)

From your dev machine:

```bash
cd /path/to/bamboo
docker build -t ghcr.io/hanfour/bamboo-relay:latest -f infra/relay/Dockerfile .
docker push ghcr.io/hanfour/bamboo-relay:latest
```

If you run from source instead, uncomment the `build:` block in
`infra/relay/docker-compose.yml` and copy the bamboo source tree to
the VPS instead of pulling the image.

### 2. Copy `infra/relay/` to the VPS

```bash
scp -r infra/relay vps:/opt/bamboo-relay
ssh vps
cd /opt/bamboo-relay
```

### 3. Configure secrets

```bash
cp .env.example .env
$EDITOR .env
```

Set:

```bash
RELAY_HOSTNAME=relay.yourdomain.com
BAMBOO_RELAY_SHARED_SECRET=<your controller's auth.session_secret>
```

### 4. Open ports + boot

```bash
sudo ufw allow 80/tcp     # Caddy needs this for HTTP-01 cert issuance
sudo ufw allow 443/tcp
docker compose up -d
docker compose logs -f
```

The first boot takes ~30s while Caddy obtains a Let's Encrypt
certificate. Watch for `certificate obtained successfully`. Once you
see `relay listening`, the relay is live.

### 5. Verify from your dev machine

```bash
# Plain HTTPS health check
curl https://relay.yourdomain.com/healthz
# -> ok

# Version probe
curl https://relay.yourdomain.com/version
# -> {"service":"bamboo-relay","version":"..."}

# WebSocket upgrade negotiation (will fail at handshake, but proves
# TLS + WS routing both work)
curl -i -H "Upgrade: websocket" -H "Connection: Upgrade" \
     -H "Sec-WebSocket-Version: 13" \
     -H "Sec-WebSocket-Key: $(openssl rand -base64 16)" \
     https://relay.yourdomain.com/relay
# -> HTTP/1.1 101 Switching Protocols (or 400 if missing required headers)
```

### 6. Point the bamboo clients at it

**Linux CLI:**

```bash
BAMBOO_RELAY_URL=wss://relay.yourdomain.com/relay \
    sudo bamboo up --auth-key bka_...
```

**macOS / iOS apps:**

Open the app, tap ⚙ → Relay URL → enter
`wss://relay.yourdomain.com/relay`.

After you connect, peers that handshake within ~60s use the direct
STUN-discovered path. Peers that don't (e.g. the other end is on
cellular with symmetric NAT) automatically fall back to relay. The
fallback log line in `bamboo up` looks like:

```
INFO relay fallback engaged peer=<key> last_handshake=0 direct=... relay=127.0.0.1:NNNNN
```

## Operations

### Rotating the shared secret

1. Update `auth.session_secret` in the controller's config and roll the controller.
2. Update `BAMBOO_RELAY_SHARED_SECRET` in `/opt/bamboo-relay/.env`.
3. `docker compose restart relay`.

Existing client sessions continue with their already-issued JWT
until it expires (1h). New connections after the rotation use the
new secret.

### Watching logs

```bash
docker compose logs -f --tail=200 relay
```

Common patterns:

- `auth rejected` — client presented an invalid JWT. Check the
  shared secret matches.
- `client connected tenant=<tid> key=<short>` — happy path.
- `relay reports peer gone` (in client logs) — means client A
  tried to send to peer B but B isn't currently connected to the
  relay. Either B is on direct path (good) or B is offline.

### Capacity

A single t2.nano (1 vCPU / 0.5 GB RAM) relay handles ~500 simultaneous
peer sessions before bandwidth limits matter. WireGuard packet sizes
are small (~80 B handshakes, ~1500 B max payload); the bottleneck on
small VPS is usually the upstream bandwidth quota rather than CPU.

## What's not in this PR (Phase 2 follow-up)

- Multi-relay failover. Today's clients only know about one relay;
  if it goes down they lose the relay path until manually
  reconfigured.
- Geographic relay selection. Add multiple rows to `relay_servers`
  and the controller will distribute them all on register, but
  clients pick the first one rather than the lowest-latency.
- Bandwidth metering for billing / capacity planning.
