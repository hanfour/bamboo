// SPDX-License-Identifier: AGPL-3.0-or-later

//go:build linux

package nat64egress

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/vishvananda/netlink"
)

// newManager returns the real Linux Tayga manager.
func newManager() Manager {
	return &linuxManager{
		lookPath: exec.LookPath,
		run:      execRun,
		sysctl:   sysctlFS{root: "/"},
	}
}

// execRun is the production runner: it captures combined output and folds
// it into the error so a failed iptables/tayga/apt-get is diagnosable.
// It lives in the Linux file because the real manager is its only caller
// (tests inject a fake runner).
func execRun(ctx context.Context, name string, args ...string) ([]byte, error) {
	out, err := exec.CommandContext(ctx, name, args...).CombinedOutput()
	if err != nil {
		return out, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

// linuxManager drives a Tayga NAT64 translator on this host. Up converges
// to "installed + configured + TUN up + routed + forwarding + MASQUERADE +
// running"; Down reverses everything Up created (leaving the package and
// config file in place — cheap to re-Up). All mutating methods hold mu.
type linuxManager struct {
	mu      sync.Mutex
	running bool
	sup     *supervisor
	prior   map[string]string // sysctl values recorded on Up, restored on Down
	v4Pool  netip.Prefix      // remembered for the Down MASQUERADE -D
	wan     string            // resolved uplink, remembered for Down

	// seams (production defaults set in newManager; overridable in tests)
	lookPath lookPathFn
	run      runner
	sysctl   sysctlFS
}

func (m *linuxManager) Up(prefix netip.Prefix, v4Pool netip.Prefix, wanIface string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return nil // already converged
	}
	ctx := context.Background()

	// 1. Ensure tayga is installed.
	if err := EnsureTayga(ctx, m.lookPath, m.run); err != nil {
		return err
	}

	// 2. Write the config.
	conf, err := RenderTaygaConfig(prefix, v4Pool)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(ConfigDir, 0o750); err != nil {
		return fmt.Errorf("mkdir %s: %w", ConfigDir, err)
	}
	if err := os.WriteFile(ConfigPath, []byte(conf), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", ConfigPath, err)
	}

	// 3. Create + bring up the TUN. Skip --mktun if it already exists
	// (a prior partial Up may have created it) so retries don't wedge.
	if _, err := netlink.LinkByName(TunDevice); err != nil {
		if _, err := m.run(ctx, "tayga", mktunArgs(ConfigPath)...); err != nil {
			return fmt.Errorf("tayga --mktun: %w", err)
		}
	}
	link, err := netlink.LinkByName(TunDevice)
	if err != nil {
		return fmt.Errorf("link %s: %w", TunDevice, err)
	}
	if err := netlink.LinkSetUp(link); err != nil {
		return fmt.Errorf("link up %s: %w", TunDevice, err)
	}

	// 4. Route the NAT64 /96 and the v4 pool into the TUN.
	if err := routeAdd(link, prefix); err != nil {
		return err
	}
	if err := routeAdd(link, v4Pool); err != nil {
		return err
	}

	// 5. Enable forwarding. Record each key's prior value only the first
	// time we see it: a partial Up that already flipped a key to "1" must
	// not re-record "1" as the original on retry, or Down would restore
	// the wrong value. Down clears m.prior, so the next fresh cycle
	// re-captures. Set is idempotent.
	if m.prior == nil {
		m.prior = map[string]string{}
	}
	for _, k := range forwardingKeys {
		if _, recorded := m.prior[k]; !recorded {
			old, err := m.sysctl.Get(k)
			if err != nil {
				return fmt.Errorf("read sysctl %s: %w", k, err)
			}
			m.prior[k] = old
		}
		if err := m.sysctl.Set(k, "1"); err != nil {
			return fmt.Errorf("set sysctl %s: %w", k, err)
		}
	}

	// 6. Source-NAT the pool out of the WAN (check-then-add = idempotent).
	wan, err := ResolveWANIface(ctx, wanIface, m.run)
	if err != nil {
		return err
	}
	if _, err := m.run(ctx, "iptables", masqueradeArgs("-C", v4Pool, wan)...); err != nil {
		if _, err := m.run(ctx, "iptables", masqueradeArgs("-A", v4Pool, wan)...); err != nil {
			return fmt.Errorf("iptables masquerade add: %w", err)
		}
	}

	// 7. Supervise the tayga daemon.
	m.sup = newSupervisor(func(ctx context.Context) (runnable, error) {
		cmd := exec.CommandContext(ctx, "tayga", daemonArgs(ConfigPath)...)
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		return cmd, nil
	})
	m.sup.Start()

	m.v4Pool, m.wan, m.running = v4Pool, wan, true
	return nil
}

// Down reverses Up. Best-effort: it attempts every teardown step and
// joins the errors so one failure does not strand the rest.
func (m *linuxManager) Down() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		// Never fully converged (no successful Up since the last Down), so
		// there is nothing we recorded to reverse. A partial Up that flipped
		// a sysctl or created the TUN before erroring is left for the next
		// Up to forward-heal (idempotent), per the package's self-heal model;
		// it is not torn down here. C3 may report such stuck egresses.
		return nil
	}
	ctx := context.Background()
	var errs []error

	// 7. Stop the supervised process.
	if m.sup != nil {
		m.sup.Stop()
		m.sup = nil
	}

	// 6. Delete the MASQUERADE rule.
	if _, err := m.run(ctx, "iptables", masqueradeArgs("-D", m.v4Pool, m.wan)...); err != nil {
		errs = append(errs, fmt.Errorf("iptables -D: %w", err))
	}

	// 5. Restore the recorded sysctls.
	for k, v := range m.prior {
		if err := m.sysctl.Set(k, v); err != nil {
			errs = append(errs, fmt.Errorf("restore sysctl %s: %w", k, err))
		}
	}
	m.prior = nil

	// 3+4. Tearing down the TUN also drops its routes.
	if link, err := netlink.LinkByName(TunDevice); err == nil {
		if err := netlink.LinkDel(link); err != nil {
			errs = append(errs, fmt.Errorf("link del %s: %w", TunDevice, err))
		}
	}

	m.running = false
	return errors.Join(errs...)
}

// routeAdd installs a scope-link route for dst into the given link,
// tolerating an "already exists" error (idempotent), mirroring
// clients/core/device's route handling.
func routeAdd(link netlink.Link, dst netip.Prefix) error {
	r := &netlink.Route{
		LinkIndex: link.Attrs().Index,
		Dst: &net.IPNet{
			IP:   dst.Addr().AsSlice(),
			Mask: net.CIDRMask(dst.Bits(), dst.Addr().BitLen()),
		},
		Scope: netlink.SCOPE_LINK,
	}
	if err := netlink.RouteAdd(r); err != nil && !isExistErr(err) {
		return fmt.Errorf("route add %s: %w", dst, err)
	}
	return nil
}

// isExistErr reports the netlink "already exists" error by string (keeps
// us decoupled from the syscall package), matching device_linux.go.
func isExistErr(err error) bool {
	return err != nil && err.Error() == "file exists"
}
