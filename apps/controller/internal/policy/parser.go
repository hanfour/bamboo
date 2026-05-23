// SPDX-License-Identifier: AGPL-3.0-or-later

package policy

import (
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/gohcl"
	"github.com/hashicorp/hcl/v2/hclparse"
)

// rawDocument is the gohcl-decoded shape of an ACL document.
type rawDocument struct {
	Rules     []rawRule           `hcl:"rule,block"`
	TagOwners map[string][]string `hcl:"tagOwners,optional"`
	// Groups maps a "group:NAME" key to the email list of its
	// members (issue #139 follow-up, §3a roadmap). Used to expand
	// "group:NAME" references inside tagOwners owner lists at
	// lookup time. Flat by design — a group may not reference
	// another group in v1. Optional; absent ⇒ TagOwners owner
	// lists must list emails (or "*") directly, matching pre-
	// #139-follow-up behaviour.
	Groups map[string][]string `hcl:"groups,optional"`
}

type rawRule struct {
	ID string `hcl:",label"`
	// All fields are marked optional at the gohcl layer so that decoding
	// never fails fast on a missing argument; parseRule below collects
	// all problems into a single aggregated error.
	Action       string   `hcl:"action,optional"`
	Sources      []string `hcl:"sources,optional"`
	Destinations []string `hcl:"destinations,optional"`
	Description  string   `hcl:"description,optional"`
}

// Parse turns DSL source into a Policy. Returns an aggregated error
// listing every diagnostic so users see all problems at once.
func Parse(filename string, source string) (*Policy, error) {
	parser := hclparse.NewParser()
	file, diags := parser.ParseHCL([]byte(source), filename)
	if diags.HasErrors() {
		return nil, diags
	}

	var doc rawDocument
	if d := gohcl.DecodeBody(file.Body, nil, &doc); d.HasErrors() {
		return nil, d
	}

	out := &Policy{
		Rules:     make([]Rule, 0, len(doc.Rules)),
		TagOwners: doc.TagOwners,
		Groups:    doc.Groups,
	}
	var errs hcl.Diagnostics

	for i, raw := range doc.Rules {
		r, ruleDiags := parseRule(raw, i)
		errs = append(errs, ruleDiags...)
		if !ruleDiags.HasErrors() {
			out.Rules = append(out.Rules, r)
		}
	}

	// groups + tagOwners cross-validation. Two classes of issue we
	// surface as Parse errors (rather than ignoring at lookup time)
	// because a policy with a typo'd group reference is almost
	// certainly a mistake the operator wants to know about before
	// PutPolicy commits the revision:
	//
	//  1. A "group:NAME" entry in tagOwners that doesn't have a
	//     matching definition in the groups map.
	//  2. A groups map key that doesn't start with the "group:"
	//     prefix (e.g. just "engineering"). Without the prefix the
	//     parser can't tell apart group references from raw emails
	//     in owner lists, so reject loudly at parse time.
	errs = append(errs, validateGroupsAndTagOwners(out.Groups, out.TagOwners)...)

	if errs.HasErrors() {
		return nil, errs
	}
	return out, nil
}

// validateGroupsAndTagOwners runs the cross-document checks that
// rawDocument's decoder can't express on its own.
func validateGroupsAndTagOwners(groups, tagOwners map[string][]string) hcl.Diagnostics {
	var diags hcl.Diagnostics
	for k := range groups {
		if !strings.HasPrefix(k, "group:") || strings.TrimPrefix(k, "group:") == "" {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "invalid group key",
				Detail: fmt.Sprintf("groups[%q]: keys must start with the \"group:\" "+
					"prefix and have a non-empty name (e.g. \"group:engineering\")", k),
			})
		}
	}
	for tag, owners := range tagOwners {
		for _, o := range owners {
			if !strings.HasPrefix(o, "group:") {
				continue
			}
			if _, ok := groups[o]; !ok {
				diags = append(diags, &hcl.Diagnostic{
					Severity: hcl.DiagError,
					Summary:  "undefined group in tagOwners",
					Detail: fmt.Sprintf("tagOwners[%q] references %q, but no such "+
						"group is defined in the groups block", tag, o),
				})
			}
		}
	}
	return diags
}

// parseRule converts a rawRule into a Rule, returning diagnostics for
// any issues. We collect rather than fail-fast so the caller sees the
// complete problem list.
func parseRule(raw rawRule, idx int) (Rule, hcl.Diagnostics) {
	var diags hcl.Diagnostics
	r := Rule{
		ID:          raw.ID,
		Description: raw.Description,
	}

	switch strings.ToLower(raw.Action) {
	case "allow":
		r.Action = ActionAllow
	case "deny":
		r.Action = ActionDeny
	case "":
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "missing action",
			Detail:   fmt.Sprintf("rule %q (#%d) is missing the required \"action\" attribute", raw.ID, idx),
		})
	default:
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "invalid action",
			Detail:   fmt.Sprintf("rule %q (#%d): action must be %q or %q, got %q", raw.ID, idx, "allow", "deny", raw.Action),
		})
	}

	if len(raw.Sources) == 0 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "missing sources",
			Detail:   fmt.Sprintf("rule %q (#%d) must declare at least one source", raw.ID, idx),
		})
	}
	if len(raw.Destinations) == 0 {
		diags = append(diags, &hcl.Diagnostic{
			Severity: hcl.DiagError,
			Summary:  "missing destinations",
			Detail:   fmt.Sprintf("rule %q (#%d) must declare at least one destination", raw.ID, idx),
		})
	}

	for _, s := range raw.Sources {
		m, err := parseSource(s)
		if err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "invalid source matcher",
				Detail:   fmt.Sprintf("rule %q: source %q: %v", raw.ID, s, err),
			})
			continue
		}
		r.Sources = append(r.Sources, m)
	}

	for _, d := range raw.Destinations {
		m, err := parseDestination(d)
		if err != nil {
			diags = append(diags, &hcl.Diagnostic{
				Severity: hcl.DiagError,
				Summary:  "invalid destination matcher",
				Detail:   fmt.Sprintf("rule %q: destination %q: %v", raw.ID, d, err),
			})
			continue
		}
		r.Destinations = append(r.Destinations, m)
	}

	return r, diags
}

// parseSource accepts a source matcher in DSL string form.
func parseSource(s string) (Matcher, error) {
	if s == "*" {
		return Matcher{Kind: MatcherWildcard}, nil
	}
	prefix, name, ok := strings.Cut(s, ":")
	if !ok {
		return Matcher{}, fmt.Errorf("missing prefix (expected one of tag:, group:, user:, cidr:)")
	}
	if name == "" {
		return Matcher{}, fmt.Errorf("empty %s name", prefix)
	}
	switch prefix {
	case "tag":
		return Matcher{Kind: MatcherTag, Name: name}, nil
	case "group":
		return Matcher{Kind: MatcherGroup, Name: name}, nil
	case "user":
		return Matcher{Kind: MatcherUser, Name: name}, nil
	case "cidr":
		p, err := netip.ParsePrefix(name)
		if err != nil {
			return Matcher{}, fmt.Errorf("invalid CIDR: %w", err)
		}
		return Matcher{Kind: MatcherCIDR, CIDR: p}, nil
	default:
		return Matcher{}, fmt.Errorf("unknown matcher prefix %q", prefix)
	}
}

// parseDestination accepts a destination matcher in DSL form
// "<prefix>:<value>:<ports>", e.g. tag:staging:443,5432 or
// cidr:0.0.0.0/0:* or "*:*".
func parseDestination(s string) (Matcher, error) {
	if s == "*" {
		return Matcher{}, fmt.Errorf("destination must include a port spec (use \"*:*\" for any)")
	}

	// CIDR matchers contain a ':' inside an IPv6 prefix; safer to split
	// from the right where the port spec lives.
	idx := strings.LastIndex(s, ":")
	if idx < 0 {
		return Matcher{}, fmt.Errorf("missing port spec (use ':<port>' or ':*')")
	}
	body := s[:idx]
	portsRaw := s[idx+1:]

	m, err := parseSource(body)
	if err != nil {
		return Matcher{}, err
	}
	ports, err := parsePorts(portsRaw)
	if err != nil {
		return Matcher{}, err
	}
	m.Ports = ports
	return m, nil
}

// parsePorts handles "*", "443", "443,5432".
func parsePorts(raw string) (PortSpec, error) {
	if raw == "" {
		return PortSpec{}, fmt.Errorf("empty port spec")
	}
	if raw == "*" {
		return PortSpec{AllPorts: true}, nil
	}
	parts := strings.Split(raw, ",")
	out := PortSpec{Ports: make([]uint16, 0, len(parts))}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		n, err := strconv.ParseUint(p, 10, 16)
		if err != nil {
			return PortSpec{}, fmt.Errorf("invalid port %q", p)
		}
		out.Ports = append(out.Ports, uint16(n))
	}
	return out, nil
}
