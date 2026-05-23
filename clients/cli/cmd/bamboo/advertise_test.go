// SPDX-License-Identifier: Apache-2.0

package main

import (
	"strings"
	"testing"
)

func TestValidateAdvertisedRoutes_Valid(t *testing.T) {
	cases := [][]string{
		nil,
		{},
		{"10.0.0.0/24"},
		{"192.168.86.0/24", "10.42.0.0/16"},
		{"::/0"},                  // exit-node-style IPv6 default
		{"0.0.0.0/0"},             // exit-node-style IPv4 default
		{"2001:db8::/32"},
		{"100.64.0.0/10"},         // CGNAT, what bamboo's tailnet itself uses
	}
	for _, in := range cases {
		if err := validateAdvertisedRoutes(in); err != nil {
			t.Errorf("validateAdvertisedRoutes(%v) = %v, want nil", in, err)
		}
	}
}

func TestValidateAdvertisedRoutes_Invalid(t *testing.T) {
	// Each entry: input, expected substring in error.
	cases := []struct {
		input    []string
		wantSubs string
	}{
		{[]string{"10.0.0.0"}, "10.0.0.0"},        // no prefix
		{[]string{"10.0.0.0/33"}, "10.0.0.0/33"},  // prefix out of range
		{[]string{"not-a-cidr"}, "not-a-cidr"},
		{[]string{"10.0.0.0\\24"}, "\\24"},        // wrong slash
		{[]string{"10.0.0.0/24/extra"}, "extra"},
		{[]string{"10.0.0.0/24", "bad"}, "bad"},   // second entry bad
	}
	for _, c := range cases {
		err := validateAdvertisedRoutes(c.input)
		if err == nil {
			t.Errorf("validateAdvertisedRoutes(%v) = nil, want error", c.input)
			continue
		}
		if !strings.Contains(err.Error(), c.wantSubs) {
			t.Errorf("validateAdvertisedRoutes(%v) error = %q, want substring %q",
				c.input, err.Error(), c.wantSubs)
		}
	}
}
