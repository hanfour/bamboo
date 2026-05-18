// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"testing"

	"github.com/hanfour/bamboo/apps/controller/internal/policy"
)

// TestParse_TagOwners verifies that the HCL parser populates
// Policy.TagOwners from the optional tagOwners attribute. The block
// is intentionally rendered as an HCL map of (tag → list of emails)
// to keep the wire shape compact for the common case where one tag
// has 1-3 owners.
func TestParse_TagOwners(t *testing.T) {
	src := `
tagOwners = {
  "tag:dev"  = ["alice@example.com", "bob@example.com"]
  "tag:prod" = ["alice@example.com"]
  "tag:any"  = ["*"]
}

rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}
`
	p, err := policy.Parse("test.hcl", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(p.TagOwners); got != 3 {
		t.Errorf("len(TagOwners) = %d, want 3; got map = %v", got, p.TagOwners)
	}
	if got := p.TagOwners["tag:dev"]; len(got) != 2 || got[0] != "alice@example.com" {
		t.Errorf("tag:dev owners = %v, want [alice bob]", got)
	}
}

// TestCanAssignTag_NoTagOwnersBlock verifies the back-compat path:
// when tagOwners is absent, every tag is freely assignable.
func TestCanAssignTag_NoTagOwnersBlock(t *testing.T) {
	p := mustParse(t, `rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:dev", "alice@example.com") {
		t.Error("with no tagOwners block, CanAssignTag should allow any tag")
	}
}

// TestCanAssignTag_OwnedByCaller verifies the happy path: the
// caller's email is listed in the tag's owner row.
func TestCanAssignTag_OwnedByCaller(t *testing.T) {
	p := mustParse(t, `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:dev", "alice@example.com") {
		t.Error("alice should be allowed to assign tag:dev")
	}
}

// TestCanAssignTag_NotOwnedByCaller verifies the deny path.
func TestCanAssignTag_NotOwnedByCaller(t *testing.T) {
	p := mustParse(t, `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if p.CanAssignTag("tag:dev", "bob@example.com") {
		t.Error("bob should NOT be allowed to assign tag:dev")
	}
}

// TestCanAssignTag_Wildcard verifies that "*" in the owner list
// opens the tag to anyone authenticated. Used for tags a tenant
// considers low-risk + delegated broadly.
func TestCanAssignTag_Wildcard(t *testing.T) {
	p := mustParse(t, `
tagOwners = {
  "tag:dev" = ["*"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:dev", "bob@example.com") {
		t.Error("wildcard owner should allow bob")
	}
}

// TestCanAssignTag_CaseInsensitiveEmail verifies that the compare
// is case-insensitive on the email — OIDC providers normalize
// differently and we don't want a Google account spelled
// "Alice@example.com" to be locked out of a tag owned by
// "alice@example.com".
func TestCanAssignTag_CaseInsensitiveEmail(t *testing.T) {
	p := mustParse(t, `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:dev", "Alice@Example.COM") {
		t.Error("email compare should be case-insensitive")
	}
}

// TestCanAssignTag_UnlistedTag verifies that a tag absent from
// tagOwners falls back to allow — only declared tags are
// restricted. This preserves the back-compat path for ad-hoc tags
// admins add without bothering to declare ownership.
func TestCanAssignTag_UnlistedTag(t *testing.T) {
	p := mustParse(t, `
tagOwners = {
  "tag:dev" = ["alice@example.com"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:experimental", "bob@example.com") {
		t.Error("unlisted tag should be freely assignable")
	}
}
