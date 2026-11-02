// SPDX-License-Identifier: AGPL-3.0-or-later

package server

import (
	"strings"
	"testing"
)

func TestIngressLabelFromDomain(t *testing.T) {
	const suffix = "svc.miilink.net"
	cases := []struct {
		domain, suffix, want string
		ok                   bool
	}{
		{"yeschef.svc.miilink.net", suffix, "yeschef", true},
		{"YesChef.SVC.miilink.net.", suffix, "yeschef", true},
		{"a.b.svc.miilink.net", suffix, "", false},
		{"svc.miilink.net", suffix, "", false},
		{"yeschef.miilink.net", suffix, "", false},
		{"www.svc.miilink.net", suffix, "", false},
		{"bad_label.svc.miilink.net", suffix, "", false},
		{"yeschef.svc.miilink.net", "", "", false},
		{"", suffix, "", false},
		{"yeschef.svc.miilink.net..", suffix, "", false},
		{".svc.miilink.net", suffix, "", false},
		{"-x.svc.miilink.net", suffix, "", false},
		{"yeschefsvc.miilink.net", suffix, "", false},
		{strings.Repeat("a", 300) + ".svc.miilink.net", suffix, "", false},
	}
	for _, c := range cases {
		got, ok := ingressLabelFromDomain(c.domain, c.suffix)
		if got != c.want || ok != c.ok {
			t.Errorf("ingressLabelFromDomain(%q, %q) = %q, %v; want %q, %v", c.domain, c.suffix, got, ok, c.want, c.ok)
		}
	}
}

func TestIngressETagMatches(t *testing.T) {
	const etag = `"abc"`
	cases := []struct {
		header string
		want   bool
	}{
		{`"abc"`, true},
		{`W/"abc"`, true},
		{`"x", "abc"`, true},
		{`*`, true},
		{``, false},
		{`"abd"`, false},
		{`abc`, false},
	}
	for _, c := range cases {
		if got := ingressETagMatches(c.header, etag); got != c.want {
			t.Errorf("ingressETagMatches(%q) = %v, want %v", c.header, got, c.want)
		}
	}
}
