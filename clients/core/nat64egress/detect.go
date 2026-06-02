// SPDX-License-Identifier: AGPL-3.0-or-later

package nat64egress

import (
	"fmt"
	"strings"
)

// PackageManager is a detected distro package manager + its install
// argv prefix (the package name is appended by the caller).
type PackageManager struct {
	Name       string   // "apt-get", "dnf", "yum", "pacman"
	InstallCmd []string // e.g. {"apt-get", "install", "-y"}
}

var knownPackageManagers = []PackageManager{
	{"apt-get", []string{"apt-get", "install", "-y"}},
	{"dnf", []string{"dnf", "install", "-y"}},
	{"yum", []string{"yum", "install", "-y"}},
	{"pacman", []string{"pacman", "-S", "--noconfirm"}},
}

// DetectPackageManager returns the first known package manager whose
// binary `lookPath` resolves. lookPath is exec.LookPath in production;
// tests inject a fake. Errors when none is found.
func DetectPackageManager(lookPath func(string) (string, error)) (PackageManager, error) {
	for _, pm := range knownPackageManagers {
		if _, err := lookPath(pm.Name); err == nil {
			return pm, nil
		}
	}
	return PackageManager{}, fmt.Errorf("no known package manager (apt-get/dnf/yum/pacman) found; install tayga manually")
}

// ParseWANIface extracts the egress interface from `ip route get <dst>`
// output, i.e. the token after "dev". Returns an error if absent.
func ParseWANIface(ipRouteGetOutput string) (string, error) {
	fields := strings.Fields(ipRouteGetOutput)
	for i, f := range fields {
		if f == "dev" && i+1 < len(fields) {
			return fields[i+1], nil
		}
	}
	return "", fmt.Errorf("no 'dev <iface>' in route output: %q", ipRouteGetOutput)
}
