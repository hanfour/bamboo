// SPDX-License-Identifier: AGPL-3.0-or-later

package policy_test

import (
	"strings"
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

// TestParse_GroupsBlock verifies that the optional `groups`
// block decodes into Policy.Groups with the raw "group:NAME" keys
// preserved (the lookup path expects them in that form).
func TestParse_GroupsBlock(t *testing.T) {
	src := `
groups = {
  "group:engineering" = ["alice@example.com", "bob@example.com"]
  "group:ops"         = ["carol@example.com"]
}

tagOwners = {
  "tag:prod" = ["group:engineering", "group:ops"]
}

rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`
	p, err := policy.Parse("test.hcl", src)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := len(p.Groups); got != 2 {
		t.Errorf("len(Groups) = %d, want 2; got map = %v", got, p.Groups)
	}
	if got := p.Groups["group:engineering"]; len(got) != 2 || got[0] != "alice@example.com" {
		t.Errorf("group:engineering members = %v, want [alice bob]", got)
	}
}

// TestCanAssignTag_GroupExpansion is the happy path for §3a group
// references: tagOwners lists "group:engineering" and alice is a
// member of that group, so she may assign the tag even though her
// email never appears in tagOwners directly.
func TestCanAssignTag_GroupExpansion(t *testing.T) {
	p := mustParse(t, `
groups = {
  "group:engineering" = ["alice@example.com", "bob@example.com"]
}
tagOwners = {
  "tag:dev" = ["group:engineering"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:dev", "alice@example.com") {
		t.Error("alice (group:engineering member) should be allowed")
	}
	if !p.CanAssignTag("tag:dev", "bob@example.com") {
		t.Error("bob (group:engineering member) should be allowed")
	}
	if p.CanAssignTag("tag:dev", "outsider@example.com") {
		t.Error("non-member should NOT be allowed via the group reference")
	}
}

// TestCanAssignTag_GroupAndLiteralMix verifies that a tagOwners
// row carrying both a group reference AND a literal email is
// permissive in either lane: the literal admin still passes even
// though they're not in any group.
func TestCanAssignTag_GroupAndLiteralMix(t *testing.T) {
	p := mustParse(t, `
groups = {
  "group:engineering" = ["alice@example.com"]
}
tagOwners = {
  "tag:prod" = ["group:engineering", "ceo@example.com"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:prod", "alice@example.com") {
		t.Error("alice (engineering) should be allowed via group")
	}
	if !p.CanAssignTag("tag:prod", "ceo@example.com") {
		t.Error("ceo (literal entry) should be allowed alongside groups")
	}
	if p.CanAssignTag("tag:prod", "intern@example.com") {
		t.Error("non-member, non-literal should NOT be allowed")
	}
}

// TestCanAssignTag_GroupExpansionCaseInsensitive carries the
// case-insensitive email-compare contract through the group
// expansion path. Same rationale as TestCanAssignTag_CaseInsensitive
// Email above.
func TestCanAssignTag_GroupExpansionCaseInsensitive(t *testing.T) {
	p := mustParse(t, `
groups = {
  "group:engineering" = ["alice@example.com"]
}
tagOwners = {
  "tag:dev" = ["group:engineering"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:dev", "Alice@Example.COM") {
		t.Error("group expansion must remain case-insensitive on email compare")
	}
}

// TestCanAssignTag_GroupWildcardMember verifies that a "*" entry
// inside a group's member list keeps the wildcard semantics — the
// group becomes "any authenticated tenant member" at expansion
// time. Niche but consistent with how "*" works at the tagOwners
// top level.
func TestCanAssignTag_GroupWildcardMember(t *testing.T) {
	p := mustParse(t, `
groups = {
  "group:any" = ["*"]
}
tagOwners = {
  "tag:open" = ["group:any"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`)
	if !p.CanAssignTag("tag:open", "anyone@example.com") {
		t.Error("group member \"*\" should open the tag to any caller")
	}
}

// TestParse_RejectsUndefinedGroupReference verifies the cross-
// document validator: a tagOwners entry referencing a group that
// isn't defined in the groups map fails Parse so the bad policy
// never lands as a new revision. The diagnostic message names both
// sides so the operator can fix the typo.
func TestParse_RejectsUndefinedGroupReference(t *testing.T) {
	src := `
groups = {
  "group:engineering" = ["alice@example.com"]
}
tagOwners = {
  "tag:dev" = ["group:nonexistent"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`
	_, err := policy.Parse("test.hcl", src)
	if err == nil {
		t.Fatal("expected Parse to reject undefined group reference, got nil")
	}
	if msg := err.Error(); !strings.Contains(msg, "group:nonexistent") {
		t.Errorf("error %q should name the missing group; got: %s", msg, msg)
	}
}

// TestParse_RejectsGroupKeyMissingPrefix verifies the parser
// rejects a groups map key that doesn't start with "group:" —
// without the prefix the lookup-time expansion can't tell apart
// group references from raw emails. Loud at parse time beats a
// silent-mismatch at policy-eval time.
func TestParse_RejectsGroupKeyMissingPrefix(t *testing.T) {
	src := `
groups = {
  "engineering" = ["alice@example.com"]
}
rule "open" {
  action       = "allow"
  sources      = ["*"]
  destinations = ["*:*"]
}`
	_, err := policy.Parse("test.hcl", src)
	if err == nil {
		t.Fatal("expected Parse to reject group key without prefix, got nil")
	}
	if msg := err.Error(); !strings.Contains(msg, "group:") {
		t.Errorf("error %q should mention the required group: prefix", msg)
	}
}

// TestCanAssignTag_UndefinedGroupAtLookupTimeDenies pins the
// defensive lookup-time behaviour: if Parse-time validation is
// somehow bypassed (e.g. an older serialized Policy is wired up
// in tests without going through Parse), an undefined group at
// lookup time degrades to "zero members" rather than panicking
// or accidentally allowing.
func TestCanAssignTag_UndefinedGroupAtLookupTimeDenies(t *testing.T) {
	p := &policy.Policy{
		TagOwners: map[string][]string{
			"tag:dev": {"group:phantom"},
		},
		// Groups deliberately left nil — Parse would reject this
		// shape, but this test exercises CanAssignTag's defensive
		// path directly.
	}
	if p.CanAssignTag("tag:dev", "alice@example.com") {
		t.Error("CanAssignTag must NOT allow when the only owner is an undefined group")
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
