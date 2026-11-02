// SPDX-License-Identifier: AGPL-3.0-or-later

package main

import (
	"net/netip"
	"testing"
)

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfig_Defaults(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{"BAMBOO_INGRESS_SUFFIX": "SVC.miilink.net."}))
	if err != nil {
		t.Fatal(err)
	}
	if c.Suffix != "svc.miilink.net" || c.Listen != "10.213.0.1:8090" || c.AdminListen != "127.0.0.1:8091" ||
		c.ControllerURL != "http://127.0.0.1:8081" || c.TokenFile != "/etc/bamboo-ingress/ingress.token" {
		t.Errorf("config = %+v", c)
	}
	if len(c.TrustedProxies) != 0 {
		t.Errorf("TrustedProxies = %v, want none by default (fail safe)", c.TrustedProxies)
	}
}

func TestLoadConfig_Rejects(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"no suffix":       {},
		"bad url":         {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_CONTROLLER_URL": "ftp://x"},
		"leading dot":     {"BAMBOO_INGRESS_SUFFIX": ".svc.test"},
		"listen no port":  {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_INGRESS_LISTEN": "172.17.0.1"},
		"bad cidr":        {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_INGRESS_TRUSTED_PROXIES": "172.17.0.0/16,nope"},
		"bare ip":         {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_INGRESS_TRUSTED_PROXIES": "172.17.0.2"},
		"mapped below 96": {"BAMBOO_INGRESS_SUFFIX": "svc.test", "BAMBOO_INGRESS_TRUSTED_PROXIES": "::ffff:0.0.0.0/95"},
	} {
		if _, err := loadConfig(envOf(env)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestLoadConfig_TrustedProxies(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{
		"BAMBOO_INGRESS_SUFFIX":          "svc.test",
		"BAMBOO_INGRESS_TRUSTED_PROXIES": " 172.17.0.9/16 , fd00::/8,,",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{netip.MustParsePrefix("172.17.0.0/16"), netip.MustParsePrefix("fd00::/8")}
	if len(c.TrustedProxies) != len(want) || c.TrustedProxies[0] != want[0] || c.TrustedProxies[1] != want[1] {
		t.Errorf("TrustedProxies = %v, want %v", c.TrustedProxies, want)
	}
}

func TestLoadConfig_TrustedProxiesUnmapsIPv4MappedPrefixes(t *testing.T) {
	c, err := loadConfig(envOf(map[string]string{
		"BAMBOO_INGRESS_SUFFIX":          "svc.test",
		"BAMBOO_INGRESS_TRUSTED_PROXIES": "::ffff:172.18.0.0/112,::ffff:10.0.0.7/128,::ffff:0.0.0.0/96",
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := []netip.Prefix{
		netip.MustParsePrefix("172.18.0.0/16"),
		netip.MustParsePrefix("10.0.0.7/32"),
		netip.MustParsePrefix("0.0.0.0/0"),
	}
	if len(c.TrustedProxies) != len(want) {
		t.Fatalf("TrustedProxies = %v, want %v", c.TrustedProxies, want)
	}
	for i := range want {
		if c.TrustedProxies[i] != want[i] {
			t.Errorf("TrustedProxies[%d] = %v, want %v", i, c.TrustedProxies[i], want[i])
		}
	}
	// The unmapped prefix matches the IPv4 peer address the proxy sees.
	if !c.TrustedProxies[0].Contains(netip.MustParseAddr("172.18.3.4")) {
		t.Error("unmapped prefix does not contain 172.18.3.4")
	}
}
