# NAT64 Phase C2 — Tayga translator on the Linux egress (2026-06-02)

Second sub-project of NAT64 Phase C. C1 (#238/#239) made an approved
egress *reachable* — every ACL-permitted peer routes `<nat64_prefix>::/96`
to it over WireGuard. C2 makes the egress *actually translate*: the CLI
fully manages a Tayga NAT64 translator on the Linux egress, bringing it
up when the controller marks the peer as the active egress and tearing
it down when revoked.

This is the first sub-project that touches the **data plane** — the
first time the bamboo client manages host-level Linux networking
(a TUN device, routes, sysctls, iptables NAT, and a supervised
translator process). There is no prior client data-plane precedent
(exit-node shipped control-plane only), so C2 establishes the pattern.

## 1. Scope

- **Controller plumbing:** `RegisterResponse` gains `nat64_egress_active`
  — the controller sets it `true` when `self.nat64_egress_approved` AND
  the tenant's `dns64_enabled` master switch is on. (The resolved
  `nat64_prefix` is already on `RegisterResponse` from Phase B.)
- **CLI reaction:** the `bamboo up` daemon, which re-registers /
  heartbeats periodically, reacts to `nat64_egress_active` flipping —
  bringing the translator up or tearing it down.
- **Tayga lifecycle manager** (`clients/core/nat64egress`, Linux-only):
  detect + auto-install Tayga; write its config; create the `nat64` TUN;
  route `<nat64_prefix>::/96` + the v4 dynamic-pool into it; enable
  forwarding; source-NAT the pool to the egress's real v4 via iptables
  MASQUERADE; supervise the `tayga` process; tear it all down on revoke.
- **Config knobs** (CLI flags, host-local): `--nat64-v4-pool` (tayga's
  dynamic pool, default a private /24) and `--nat64-wan-iface`
  (MASQUERADE uplink, default = the default-route interface).

## 2. Out of scope (C2)

- Health probing of the egress + multi-egress active selection (C3).
  C1 already picks a single active egress; C2 just translates on whoever
  is told it's active.
- The DNS64 client that synthesises the v6 destination addresses (C4 /
  the deferred Phase B PR 2). C2 is verified by forcing a v6 packet
  manually (`ping6 64:ff9b::<v4>`); the synthesis is a separate consumer.
- Reporting egress translator health/errors back to the controller — a
  C3 concern. C2 fails loud locally (logs + a clear error) when it
  cannot bring the translator up.
- Stateful NAT64 (Jool). C2 uses Tayga (userspace, stateless 1:1 +
  MASQUERADE) so the CLI manages the whole lifecycle without a kernel
  module / DKMS dependency.
- Non-Linux egress. The translator manager is `//go:build linux`; other
  platforms get a no-op stub (they can't be a NAT64 egress).

## 3. Engine decision — Tayga (not Jool)

**Tayga** is a userspace NAT64 daemon that creates a TUN device and does
stateless 1:1 v6↔v4 mapping out of a configurable v4 *dynamic pool*; the
v4 side is then source-NATed (iptables MASQUERADE) onto the egress's real
address, which restores the many-v6-clients-to-one-v4 behaviour at the
NAT layer. **Jool** (kernel module, stateful) is the cleaner production
NAT64, but a CLI that fully manages the lifecycle would have to load /
install a kernel module (`jool-dkms` needs matching kernel headers + a
DKMS build) — distro-specific and fragile. Tayga keeps the entire data
plane in userspace + iptables, which the CLI can manage uniformly across
distros. The pool-sizing trade-off (concurrent-v6-client ceiling = pool
size) is acceptable for the egress role and revisitable in a later phase.

## 4. Approval visibility — `RegisterResponse.nat64_egress_active`

The CLI cannot see its own `nat64_egress_approved` today: C1 stored it
on the REST `apiPeerJSON`, not the proto, and the CLI consumes the proto
`RegisterResponse`. C2 adds the egress signal to the response, following
the Phase B pattern that already pushes `dns64_enabled` + `nat64_prefix`
there.

- **proto:** `RegisterResponse` gains `bool nat64_egress_active` (next
  free field number).
- **controller:** in the register handler, set
  `Nat64EgressActive = self.NAT64EgressApproved && tenant.DNS64Enabled`.
  Both conditions mirror C1's route gating — the egress only needs to
  translate when traffic is actually routed to it (DNS64 master on) and
  it is the approved egress. (C1 also gates on the egress being *live*;
  the registering self is live by definition of registering.)
- **CLI daemon:** the `bamboo up` loop tracks the last-seen value; on a
  `false→true` edge it calls `manager.Up(prefix, pool, wan)`, on
  `true→false` it calls `manager.Down()`. The resolved `nat64_prefix`
  comes from the same response. Idempotent: re-registers with an
  unchanged value are no-ops.

## 5. Tayga lifecycle manager — `clients/core/nat64egress`

A new package, mirroring how `clients/core/device` isolates the Linux
netlink data plane. Interface (cross-platform):

```go
// Manager owns the NAT64 translator lifecycle on this host.
type Manager interface {
    // Up ensures Tayga is installed, configured for prefix, and running,
    // with routes + forwarding + source-NAT in place. Idempotent.
    Up(prefix netip.Prefix, v4Pool netip.Prefix, wanIface string) error
    // Down tears everything C2 created back down. Idempotent.
    Down() error
}
```

`//go:build linux` provides the real `taygaManager`; `//go:build !linux`
provides a no-op that errors on `Up` ("NAT64 egress is Linux-only").

**`Up` sequence (idempotent — each step tolerates "already done"):**

1. **Ensure Tayga installed.** If `tayga` is on `PATH`, skip. Else
   detect the package manager (apt-get / dnf / yum / pacman by probing
   for the binary) and install (`apt-get install -y tayga`, etc.). If no
   known package manager or the install fails → return a clear error;
   the daemon logs it and leaves the egress non-translating.
2. **Write the config** to a CLI-owned path (`/etc/tayga/bamboo-nat64.conf`):
   `tun-device nat64`, `prefix <nat64_prefix>`, `ipv4-addr <first usable
   in v4Pool>`, `dynamic-pool <v4Pool>`, `data-dir /var/lib/tayga/bamboo`.
3. **Create + up the TUN:** `tayga --config <conf> --mktun` then
   `ip link set nat64 up` (netlink).
4. **Route into the TUN:** `ip -6 route add <nat64_prefix> dev nat64` and
   `ip route add <v4Pool> dev nat64` (netlink; ignore EEXIST).
5. **Enable forwarding:** write `net.ipv4.ip_forward=1` and
   `net.ipv6.conf.all.forwarding=1` (record prior values to restore).
6. **Source-NAT:** `iptables -t nat -C/-A POSTROUTING -s <v4Pool> -o
   <wanIface> -j MASQUERADE` (check-then-add for idempotency).
7. **Supervise `tayga`:** start `tayga --config <conf> --nodetach` as a
   managed child process; restart on unexpected exit while active; kill
   on `Down()` / daemon shutdown. Tayga dying with the bamboo daemon is
   acceptable — the egress IS the bamboo daemon.

**`Down`** reverses 7→2: stop the process, delete the MASQUERADE rule,
restore the recorded sysctls, delete the routes, tear down the TUN
(`tayga --mktun` has no rm; `ip link del nat64`). The package + config
file are left in place (cheap to re-`Up`; uninstalling a package on
revoke is over-reach).

## 6. Config knobs

- `--nat64-v4-pool` (string CIDR, default `192.168.255.0/24`): Tayga's
  dynamic pool. A private range unlikely to collide; the admin overrides
  if the egress box already uses it. Validated as a v4 CIDR at flag parse.
- `--nat64-wan-iface` (string, default `""` → auto-detect via the
  default route, e.g. `ip route get 1.1.1.1` → `dev`): the MASQUERADE
  uplink. Override when the box has multiple uplinks.

Both are passed from the daemon into `manager.Up`. They are host-local
(not controller-pushed) — the egress operator owns this box.

## 7. Data flow (the manual E2E)

A mesh peer sends a packet to `64:ff9b::<v4>` (DNS64-synthesised in C4,
or a manual `ping6 64:ff9b::<hex-of-v4>` for the C2 test):
1. C1's `allowedIPsFor` put `<nat64_prefix>::/96 → egress` in the peer's
   WireGuard `AllowedIps`, so the packet tunnels to the egress.
2. On the egress, `ip -6 route` sends `<nat64_prefix>::/96` into the
   `nat64` TUN.
3. Tayga translates 6→4 (the embedded v4 is the destination; the v6
   source maps to a v4 from the dynamic pool).
4. iptables MASQUERADE rewrites the pool source to the egress's real v4.
5. The v4 packet reaches the destination; the reply retraces the path.

**C2's done test:** two peers + a designated egress on real hardware;
from a non-egress peer, `ping6 64:ff9b::<hex of a reachable v4>` succeeds
(egress translates), and `tcpdump` on the egress shows the v4 packet
leaving the WAN with the egress's source v4.

## 8. Error / edge cases

- **Tayga uninstallable** (unknown distro, no network, locked dpkg) →
  `Up` returns a clear error; daemon logs `nat64 egress: <err>` and the
  peer stays reachable on the route (C1) but black-holes the translation
  — same failure surface as "approved but no translator yet". A future
  phase reports this to the controller (C3).
- **`nat64_egress_active` flaps** across re-registers → `Up`/`Down` are
  idempotent; only true edges act, so a steady-state re-register is a
  no-op.
- **v4 pool collides** with the box's LAN → admin sets `--nat64-v4-pool`.
  C2 does not auto-detect collisions (YAGNI; documented).
- **Non-root** → the netlink/iptables/sysctl/install steps fail; `Up`
  returns the underlying permission error. The egress is expected to run
  bamboo as root (it already needs root for the WireGuard device).
- **Daemon crash** → tayga (supervised child) dies with it; on restart
  the next register re-applies `Up`. No persistent half-state because
  `Down`-on-shutdown + idempotent `Up` converge.
- **Non-Linux** → the no-op manager errors on `Up`; the controller can
  still mark a non-Linux peer active (admin error), but it simply won't
  translate. (C1's capability flag is advisory; a hard "Linux only"
  gate is a possible C3 hardening.)

## 9. Test plan

**Unit-testable (pure / mockable, in CI):**
- Tayga config generator: `(prefix, v4Pool) → conf string` — golden test
  for `64:ff9b::/96` + `192.168.255.0/24`.
- Package-manager detection: given a fake `PATH`/probe func, picks
  apt-get / dnf / yum / pacman, errors when none.
- WAN-iface resolution: parse `ip route get` output → device name.
- Daemon edge logic: a fake `Manager`; assert `Up` on false→true,
  `Down` on true→false, no-op on steady state.
- Controller: `nat64_egress_active = approved && dns64_enabled`
  (register integration test against real Postgres).

**Not unit-testable (root + real host):**
- The actual install / TUN / routes / sysctl / MASQUERADE / process —
  `//go:build linux`, verified by `GOOS=linux go vet` (compiles) + the
  §7 manual E2E on hardware.

## 10. PR breakdown (≈2)

1. **Plumbing + pure functions** (controller + CLI, fully CI-verifiable):
   proto `RegisterResponse.nat64_egress_active`; controller sets it;
   the `nat64egress.Manager` interface + the `!linux` no-op + the pure
   helpers (config generator, pkg-mgr detect, wan parse); the daemon
   edge-detection wiring calling the interface (with the real Linux impl
   stubbed to return "not implemented" so the daemon compiles + unit
   tests pass); the two CLI flags. Unit + integration tests.
2. **Linux Tayga manager** (`//go:build linux`): the real `Up`/`Down`
   (install, TUN, routes, sysctl, MASQUERADE, process supervision) +
   teardown; `GOOS=linux` compile + the manual hardware E2E.

## 11. Phase boundary

C2 makes the egress translate. C3 adds health probing (is the translator
actually up?) + multi-egress selection + reporting status to the
controller. C4 (the deferred Phase B PR 2) generates the synthesised v6
traffic automatically via the Apple DNS64 resolver. Until C4, C2 is
exercised by manually targeting `64:ff9b::<v4>`.
