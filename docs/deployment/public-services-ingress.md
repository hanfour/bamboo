# Public services ingress: operator guide

Design: `docs/design/2026-11-02-public-services-ingress.md`. This page is
the single-VPS (`infra/full`) runbook. Examples use `bamboo.example.com` for
the control plane and `svc.example.com` for the public suffix (production:
`svc.miilink.net`). Replace `<static-ip>` with the VPS's static IP.

The feature is off until you opt in. With `INGRESS_SUFFIX`,
`BAMBOO_INGRESS_SIGNING_KEY` and `COMPOSE_PROFILES` unset, `docker compose up -d`
and `./deploy.sh` start the same six services as before.

## What runs where

```
client ─https─> VPS :443 Caddy (on-demand cert; ask → controller:8081)
                  └─ http ─> 10.213.0.1:8090 bamboo-ingress (host network)
                                └─ http over WireGuard ─> <peer mesh IP>:<port>
```

- DNS `*.svc.example.com` points at the VPS.
- Caddy (existing container) terminates TLS. A certificate is issued per
  name, on demand, only when
  `GET http://controller:8081/internal/v1/ingress/ask?domain=<name>` answers
  200, which it does only for a live service whose tenant holds a current,
  unexpired ingress token (step 6). There is no wildcard certificate.
- `bamboo-ingress` (compose service `ingress`, `network_mode: host`) listens
  on `10.213.0.1:8090`, the gateway of the `ingress_edge` bridge
  (`br-bamboo-edge`, subnet `10.213.0.0/24`). It polls the controller every
  10 s over `127.0.0.1:8081` (the controller's host port,
  `BAMBOO_CONTROLLER_HOST_PORT`, default 8081) and proxies to `<peer mesh IP>:<port>` over the
  ingress peer's WireGuard interface. Admin listener `127.0.0.1:8091`
  (`/healthz`, `/version`, `/metrics`).
- The controller publishes its REST port on `127.0.0.1:8081` only. Never
  change this to `0.0.0.0`: the listener has no TLS and the `ask` endpoint
  needs no token. If another process on the host already uses 8081, set
  `BAMBOO_CONTROLLER_HOST_PORT` in `.env` to a free port; compose publishes
  that port and points the ingress's `BAMBOO_CONTROLLER_URL` at it. The
  container port stays 8081, so Caddy's `ask` URL does not change.
- The VPS is an ordinary mesh peer (`bamboo install-service`) tagged
  `ingress`, in the same tenant as the services it serves. One ingress peer
  serves one tenant.

### Why the ingress trusts exactly `10.213.0.2`

`bamboo-ingress` uses `X-Forwarded-For` only on connections from
`BAMBOO_INGRESS_TRUSTED_PROXIES`, which compose sets to `10.213.0.2/32`,
Caddy's fixed address on `ingress_edge`.

Caddy resolves `host.docker.internal` to `10.213.0.1`. That address is on a
subnet Caddy is directly attached to, so the packet leaves through Caddy's
`ingress_edge` interface with source `10.213.0.2`, whatever network holds
Caddy's default route. On the host, `10.213.0.1` is a local address, so the
packet is delivered through the INPUT path. Docker's MASQUERADE rules are in
`nat POSTROUTING`, which only sees forwarded and outgoing packets, so the
source is not rewritten. The ingress sees `10.213.0.2`, not a bridge gateway.
This was checked with Docker 29: a host-network listener on `10.213.0.1`
reported the client as `10.213.0.2`.

The plan's first draft used `host-gateway` (`172.17.0.1`, the default
`docker0` bridge). That also arrives un-NATed, but from Caddy's address on
whichever network holds its default route. Pinning that would mean pinning
the compose `default` network, and compose recreates a network whose
settings change, which restarts every container on it. The separate
`ingress_edge` bridge avoids that.

Other containers on the host can also open connections to `10.213.0.1:8090`
(it is a host address). They arrive from their own addresses, so their
`X-Forwarded-For` is ignored and they are rate-limited by their own IP.

## Enable

Do these in order. Steps 1 to 4 change nothing that users see.

### 0. Pre-deploy checks

```bash
docker version --format '{{.Server.Version}}'   # 28 or newer
sudo ss -ltnp | grep -E ':(8081|8091|8090)\b'     # expect no output
ip route | grep 10.213.0                        # expect no output
```

- Docker 28 or newer: older engines had an L2-neighbour reachability issue
  for ports published on `127.0.0.1`, which the controller's port relies on.
- 8081 is the controller's host port (or your `BAMBOO_CONTROLLER_HOST_PORT`);
  8091 and 8090 are the ingress's admin and proxy listeners. A line here
  means another process holds the port: stop it, or for 8081 pick another
  `BAMBOO_CONTROLLER_HOST_PORT`. `deploy.sh` runs the same check and aborts,
  before touching the running controller, when one of these ports is held
  by something other than this stack (8091 and 8090 only once
  `COMPOSE_PROFILES` includes `ingress`).
- `10.213.0.0/24` must not be in use on the host (see step 3).

### 1. DNS (manual)

At your DNS provider add:

```
*.svc.example.com.  A  <static-ip>
```

Check: `dig +short anything.svc.example.com` prints `<static-ip>`.

### 2. Controller key and suffix

```bash
cd infra/full
docker compose run --rm controller relaykey
```

Put the SIGNING value in `.env` as `BAMBOO_INGRESS_SIGNING_KEY=`. It must
differ from `BAMBOO_RELAY_SIGNING_KEY`; generate a new pair, do not reuse the
relay one. Also set `INGRESS_SUFFIX=svc.example.com`.

### 3. Deploy and recreate Caddy once

The ingress image is published per release. `BAMBOO_IMAGE_TAG` must name a
release that includes `ghcr.io/hanfour/bamboo-ingress`.

```bash
./deploy.sh
docker compose up -d caddy
```

`deploy.sh` recreates the controller with the new environment and the
loopback port, and reloads the Caddyfile. `docker compose up -d caddy`
recreates Caddy so it joins `ingress_edge` and sees `INGRESS_SUFFIX`; ports
80 and 443 are down for the few seconds the container takes to restart.

Check:

```bash
ip -4 addr show br-bamboo-edge          # inet 10.213.0.1/24
docker compose exec caddy printenv INGRESS_SUFFIX
curl -s -o /dev/null -w '%{http_code}\n' \
  'http://127.0.0.1:8081/internal/v1/ingress/ask?domain=nothing.svc.example.com'   # 404
curl -s -o /dev/null -w '%{http_code}\n' https://bamboo.example.com/internal/v1/ingress/ask   # 404 (public guard)
```

If `ip route` on the host already uses `10.213.0.0/24`, change every
`10.213.0.x` in `docker-compose.yml` together before this step.

### 4. Ingress peer (tag `ingress`)

First check whether `bamboo0` is already in use:

```bash
systemctl status bamboo --no-pager    # an installed bamboo client service
systemctl status wg-quick@bamboo0 --no-pager
```

- If the bamboo client service is already running on the VPS, reuse that
  peer: give it the `ingress` tag (Peers page) and go to step 5.
- If `wg-quick@bamboo0` is running, the VPS is a hand-configured mesh hub
  (Path A in `dogfood-aws-lightsail-walkthrough.md`). Do not reuse it: that
  interface is not driven by the controller. Install the client on a
  different interface, `bamboo1`, as shown below.

In the Web UI, Peers, Add peer: Reusable off, Auto-approve on, Tags
`ingress`. Copy the key, then on the VPS:

```bash
# Interface bamboo0 is free:
curl -fsSL https://bamboo.example.com/install.sh \
  | sudo env BAMBOO_AUTH_KEY='bka_...' BAMBOO_HOSTNAME=vps-ingress sh

# bamboo0 is taken by the hub: install the binary only, then the service on bamboo1.
curl -fsSL https://bamboo.example.com/install.sh | sudo env BAMBOO_NO_SERVICE=1 sh
sudo env BAMBOO_AUTH_KEY='bka_...' bamboo install-service \
  --server https://bamboo.example.com --hostname vps-ingress --iface bamboo1
```

`install-service` writes `/etc/bamboo/bamboo.env` (0600, includes the
interface) and starts the systemd unit, which runs `bamboo up`.

Check:

```bash
sudo bamboo status
sudo wg show bamboo1          # or bamboo0
ip route get <target peer mesh IP>   # must say "dev bamboo1" (or bamboo0)
```

With the hub on the same box, the hub's `100.64.0.1/24` route and the
client's per-peer `/32` routes coexist; the `/32` is more specific and wins.
Confirm with `ip route get` as above.

### 5. Host firewall

Two rules. Use `ING=bamboo1` (or `bamboo0`) for the ingress peer's interface.

a) The ingress peer's WireGuard interface accepts only replies. The ingress
opens connections to the targets; nothing on the mesh needs to open a
connection to the VPS through this interface. Without this rule a target
peer, which has the ingress peer's address in its AllowedIPs, could reach
sshd and any other port the VPS listens on.

b) If the host's INPUT policy drops new connections (ufw active, or an
nftables input chain with `policy drop`), allow Caddy to reach the ingress
listener.

Which firewall the VPS uses is not recorded in this repo: `bootstrap.sh`
installs `iptables-persistent` and adds iptables rules, while
`single-vps.md` uses ufw. Check with `sudo ufw status` and
`sudo nft list ruleset | head`. Use one of the following.

iptables (the `bootstrap.sh` convention, persisted by iptables-persistent):

```bash
ING=bamboo1
sudo iptables  -I INPUT 1 -i "$ING" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
sudo iptables  -I INPUT 2 -i "$ING" -j DROP
sudo ip6tables -I INPUT 1 -i "$ING" -m conntrack --ctstate ESTABLISHED,RELATED -j ACCEPT
sudo ip6tables -I INPUT 2 -i "$ING" -j DROP
# Only if the INPUT policy is DROP:
sudo iptables -I INPUT 1 -i br-bamboo-edge -s 10.213.0.2 -d 10.213.0.1 -p tcp --dport 8090 -j ACCEPT
sudo netfilter-persistent save
```

ufw (its before-rules already accept established and related traffic, so a
plain deny on the interface leaves only replies):

```bash
ING=bamboo1
sudo ufw deny in on "$ING"
sudo ufw allow in on br-bamboo-edge from 10.213.0.2 to 10.213.0.1 port 8090 proto tcp
```

nftables (a separate table; a drop here is final, an accept falls through to
your other chains):

```bash
sudo nft add table inet bamboo_ingress
sudo nft 'add chain inet bamboo_ingress input { type filter hook input priority filter - 1 ; policy accept ; }'
sudo nft add rule inet bamboo_ingress input iifname "bamboo1" ct state established,related accept
sudo nft add rule inet bamboo_ingress input iifname "bamboo1" drop
# Persist: add the same table to /etc/nftables.conf.
```

c) If the VPS also runs the Path A hub on `bamboo0` (step 4), drop traffic
to the ingress listener's address that arrives from the hub mesh, so a hub
peer that routes `10.213.0.1` through the hub cannot reach the ingress
directly:

```bash
sudo iptables -I INPUT 1 -i bamboo0 -d 10.213.0.1 -j DROP
sudo netfilter-persistent save
# ufw:      sudo ufw deny in on bamboo0 to 10.213.0.1
# nftables: sudo nft add rule inet bamboo_ingress input iifname "bamboo0" ip daddr 10.213.0.1 drop
```

These rules touch only INPUT. The hub's `FORWARD -i bamboo0 -o bamboo0`
rule and Docker's own rules are unaffected. The WireGuard UDP port arrives
on the public interface and is unaffected too.

Check from the VPS: `curl -s 127.0.0.1:8091/healthz` still works after
step 7; from a target peer, `nc -vz <VPS ingress mesh IP> 22` times out.

### 6. Ingress token

Web UI, Settings, Public services (`/ingress`), Ingress service token:
choose the `vps-ingress` peer, Issue token. The token is shown once. On the
VPS:

```bash
sudo install -d -o root -g root -m 0755 /etc/bamboo-ingress
sudo sh -c 'umask 077; cat > /etc/bamboo-ingress/ingress.token'   # paste, Enter, Ctrl-D
sudo chown 65532:65532 /etc/bamboo-ingress/ingress.token
sudo chmod 0600 /etc/bamboo-ingress/ingress.token
sudo ls -ln /etc/bamboo-ingress/      # -rw------- 1 65532 65532 ... ingress.token
```

65532 is the `nonroot` user of the distroless image. Compose mounts the
directory read-only at `/etc/bamboo-ingress` and sets
`BAMBOO_INGRESS_TOKEN_FILE=/etc/bamboo-ingress/ingress.token`. The ingress
re-reads the file on every request to the controller, so a rotated token
(Rotate token, then the same commands) applies without a restart. Rotating
revokes the old token immediately.

### 7. Start the ingress

Add to `.env`:

```bash
COMPOSE_PROFILES=ingress
```

then:

```bash
docker compose up -d ingress
curl -s 127.0.0.1:8091/healthz                          # ok
curl -s 127.0.0.1:8091/metrics | grep '^bamboo_ingress_routes '
docker compose logs --tail=50 ingress
```

From now on `./deploy.sh` pulls and recreates `ingress` on each deploy. If
`COMPOSE_PROFILES` already lists other profiles, use a comma:
`COMPOSE_PROFILES=jobs,ingress`.

### 8. First service

On the Public services page: Add service (peer, label, port), Approve,
Enable. Within 10 s `bamboo_ingress_routes` counts it. The first HTTPS
request to `https://<label>.svc.example.com` triggers certificate issuance
and takes a few seconds; watch `docker compose logs -f caddy`.

## The target service

- It must listen on the peer's mesh IP (or `0.0.0.0`) at the configured
  port (1024 to 65535). A service bound to `127.0.0.1` is not reachable.
- The ACL is enforced at L3 only (addresses, not ports), so restrict the
  service's sources to the ingress peer's mesh IP on the target host.
- It receives `X-Forwarded-For`, `X-Forwarded-Proto: https`,
  `X-Forwarded-Host`, `X-Bamboo-Ingress-Service`, and the public `Host`.
  Trust them only on connections from the ingress peer's mesh IP.
- Limits: 120 req/min per service, 300 req/min per client IP, 8 MiB request
  body, 10 min idle stream. Authentication (bearer, OAuth) is the service's
  job.
- Paths under `/internal/` return 404 at Caddy and never reach the service.

## Let's Encrypt rate limits

Each public name gets its own certificate, counted against Let's Encrypt's
limits for the registered domain (`example.com`), which the control-plane
and relay certificates share. At the time of writing the main limit is 50
new certificates per registered domain per week; renewals do not count
against it. Check https://letsencrypt.org/docs/rate-limits/ for the current
numbers.

- Creating and deleting many labels in a short time can use up the weekly
  allowance. Caddy then fails to issue for new names until it resets.
- Repeated failed validations for one name are also limited. They usually
  mean DNS for `*.svc.example.com` is wrong or port 80/443 is blocked.
- Names that the `ask` endpoint refuses never reach Let's Encrypt, so
  scanners hitting random `*.svc.example.com` names cost nothing. A tenant
  that has not issued an ingress token (or whose token expired) gets no
  certificates either.

## Monitoring

The admin listener is on the host loopback only. A Prometheus on the VPS
host (or in a container with `network_mode: host`) scrapes it:

```yaml
scrape_configs:
  - job_name: bamboo-ingress
    static_configs:
      - targets: ["127.0.0.1:8091"]
```

Useful series: `bamboo_ingress_routes`,
`bamboo_ingress_routes_last_success_timestamp_seconds` (alert when older
than a few minutes), `bamboo_ingress_routes_refresh_failures_total`,
`bamboo_ingress_requests_total`, `bamboo_ingress_request_duration_seconds`,
`bamboo_ingress_telemetry_dropped_total`.

## Disable

- One service: Disable or Delete it on the Public services page. The ingress
  drops the route at its next poll, within about 10 s, and cuts that
  service's open streams. Further requests get 404 or 502, and Caddy issues
  no new certificate for the name.
- If the controller is unreachable or answers 5xx, the ingress keeps
  serving its last good table. If it rejects the token (401/403: rotated,
  expired, or the peer lost `tag:ingress`), the ingress clears its table
  and serves 404 until a valid token is in place. To stop everything at
  once regardless, stop the container: `docker compose stop ingress`.

## Roll back

None of these steps touch postgres, clickhouse, web, relay, or the existing
Caddy sites.

1. Stop the ingress and turn the profile off:

   ```bash
   docker compose --profile ingress stop ingress
   docker compose --profile ingress rm -f ingress
   ```

   then delete `COMPOSE_PROFILES=ingress` from `.env`. Public names now get
   502 from Caddy. This is enough to take the feature offline.

2. Optional, to remove the Caddy site: delete the `*.{$INGRESS_SUFFIX:...}`
   site block (and, if you like, the global `on_demand_tls` block) from
   `infra/full/Caddyfile`, then
   `docker compose exec caddy caddy reload --config /etc/caddy/Caddyfile`.
   Leaving the site is harmless once services are disabled: `ask` refuses
   every name. Keep the control-plane `/internal/*` guard.

3. Optional: remove `INGRESS_SUFFIX` and `BAMBOO_INGRESS_SIGNING_KEY` from
   `.env` and run `./deploy.sh`. The controller's ingress endpoints then
   answer 503 and `ask` answers 404.

4. Optional, on the VPS: `sudo bamboo uninstall-service`, delete the
   `vps-ingress` peer in the Web UI, `sudo rm -r /etc/bamboo-ingress`, and
   remove the firewall rules from step 5.

5. Full revert of the stack files, to put `infra/full` back exactly as it
   was before this feature (no loopback port on the controller, Caddy off
   `ingress_edge`). Run steps 1 and 3 first, then from the repository root
   on the VPS, with `<previous-ref>` the commit or tag you deployed before:

   ```bash
   git checkout <previous-ref> -- infra/full
   cd infra/full
   docker compose up -d controller caddy     # recreate both from the old files
   docker network rm full_ingress_edge       # the bridge is now unused
   ip -4 addr show br-bamboo-edge            # "does not exist"
   ```

   `docker network rm` fails while a container is still attached; check with
   `docker network inspect full_ingress_edge`. The network name is
   `<compose project>_ingress_edge`; the project is `full` when compose runs
   from `infra/full`. Ports 80 and 443 are down for the few seconds Caddy
   takes to restart.

Migration 00024 is additive. Roll it back only together with the rest of
the release (`docker compose run --rm controller migrate down` once).
