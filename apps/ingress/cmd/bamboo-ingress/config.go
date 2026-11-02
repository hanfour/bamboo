// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

type config struct {
	Suffix         string
	Listen         string
	AdminListen    string
	ControllerURL  string
	TokenFile      string
	TrustedProxies []netip.Prefix
}

func envOr(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

// loadConfig reads the BAMBOO_INGRESS_* environment. Only the suffix is
// required; the defaults match infra/full. Trusted proxies default to
// none, so X-Forwarded-For is ignored until an operator names Caddy.
func loadConfig(getenv func(string) string) (config, error) {
	c := config{
		Suffix:        strings.TrimSuffix(strings.ToLower(strings.TrimSpace(getenv("BAMBOO_INGRESS_SUFFIX"))), "."),
		Listen:        envOr(getenv, "BAMBOO_INGRESS_LISTEN", "10.213.0.1:8090"),
		AdminListen:   envOr(getenv, "BAMBOO_INGRESS_ADMIN_LISTEN", "127.0.0.1:8091"),
		ControllerURL: envOr(getenv, "BAMBOO_CONTROLLER_URL", "http://127.0.0.1:8081"),
		TokenFile:     envOr(getenv, "BAMBOO_INGRESS_TOKEN_FILE", "/etc/bamboo-ingress/ingress.token"),
	}
	if c.Suffix == "" || strings.HasPrefix(c.Suffix, ".") || !strings.Contains(c.Suffix, ".") {
		return c, errors.New("BAMBOO_INGRESS_SUFFIX must be a domain such as svc.miilink.net")
	}
	u, err := url.Parse(c.ControllerURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return c, fmt.Errorf("BAMBOO_CONTROLLER_URL %q must be an http(s) URL", c.ControllerURL)
	}
	for _, addr := range []string{c.Listen, c.AdminListen} {
		if _, _, err := net.SplitHostPort(addr); err != nil {
			return c, fmt.Errorf("listen address %q: %w", addr, err)
		}
	}
	c.TrustedProxies, err = parsePrefixes(getenv("BAMBOO_INGRESS_TRUSTED_PROXIES"))
	if err != nil {
		return c, fmt.Errorf("BAMBOO_INGRESS_TRUSTED_PROXIES: %w", err)
	}
	return c, nil
}

// parsePrefixes parses a comma-separated CIDR list; empty items are
// skipped and host bits are masked off.
func parsePrefixes(s string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, item := range strings.Split(s, ",") {
		if item = strings.TrimSpace(item); item == "" {
			continue
		}
		p, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR (write a single address as a.b.c.d/32)", item)
		}
		if p, err = unmapPrefix(p); err != nil {
			return nil, fmt.Errorf("%q: %w", item, err)
		}
		out = append(out, p.Masked())
	}
	return out, nil
}

// unmapPrefix turns an IPv4-mapped prefix (::ffff:a.b.c.d/n) into the
// IPv4 prefix it denotes, because the proxy compares unmapped peer
// addresses and a mapped prefix would never match. A mapped prefix
// shorter than /96 also spans non-mapped IPv6 space, so it is refused.
func unmapPrefix(p netip.Prefix) (netip.Prefix, error) {
	if !p.Addr().Is4In6() {
		return p, nil
	}
	if p.Bits() < 96 {
		return netip.Prefix{}, errors.New("IPv4-mapped prefix shorter than /96 is ambiguous; use an IPv4 CIDR")
	}
	return netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96), nil
}
