# Ten minutes to two peers

This is the path from "the stack is up" to "Alice's laptop can reach
the database, and Carol's laptop cannot." It assumes the controller,
web, and relay are already running. For a laptop, follow
[local.md](./local.md). For a VPS, follow [single-vps.md](./single-vps.md)
through the health check, then come back here.

The relay is in that stack so a peer that cannot punch through NAT
still has a path. This playbook checks the control plane, which is
what decides the WireGuard `AllowedIPs` those peers install.

## What a peer is allowed to match

| How the node registered | Owner | Tags |
| --- | --- | --- |
| User session (the person signed in and enrolled their own machine) | That user | None, until someone sets them |
| Pre-auth key | The admin who minted the key | The tags on the key, copied at register |
| Dev fallback (`BAMBOO_REQUIRE_AUTH=false`, no credential) | None | None |

`user:` matches the owner's email. `group:` matches a `groups` entry
that lists that email. A peer with no owner matches only `tag:` and
`cidr:`. A service enrolled with Alice's key is Alice's machine for
policy purposes. Give the key a tag when the service should be named
on its own, for example `tag:db`.

## 1. Write the policy

In the admin UI's ACL editor, or with `PUT /api/v1/policy`, save:

```hcl
groups = {
  "group:engineering" = ["alice@example.com"]
  "group:dba"         = ["dba@example.com"]
}

rule "eng-to-databases" {
  action       = "allow"
  sources      = ["group:engineering"]
  destinations = ["group:dba:*", "tag:db:*"]
}
```

`group:dba` is every peer owned by a member of that group. `tag:db`
is every peer carrying that tag, including one with no owner.

## 2. Enroll the database

Sign in as the DBA (the account whose email is in `group:dba`) and
mint a pre-auth key. The JSON body the UI's create call does not send
yet, so use the API when the service needs a tag:

```bash
curl -s -X POST "$CONTROLLER/api/v1/preauth-keys" \
  -H "Authorization: Bearer $DBA_SESSION" \
  -H "Content-Type: application/json" \
  -d '{"description":"db","reusable":true,"autoApprove":true,"tags":["db"]}'
```

The response includes `secret` once. On the database host:

```bash
bamboo up --login-server "$CONTROLLER" --preauth-key "$SECRET" --hostname db-server
```

That peer's owner is the DBA, and its tags are `db`.

## 3. Enroll the laptops

Alice signs in and enrolls her own machine with that session (the web
UI's add-device flow, or a client that sends her session bearer). The
peer belongs to `alice@example.com`, so `group:engineering` matches it.

Carol enrolls the same way. She is in neither group, so the rule does
not match her.

## 4. Check the wire, not the editor

Re-register, or wait for the client's next policy refresh, then read
Alice's peer list. The database peer's `allowedIps` contains the
database tunnel address (`/32`, and the IPv6 `/128` when the peer has
one). Carol's view of the same peer has an empty `allowedIps`, which
means her client does not install that peer.

```bash
curl -s "$CONTROLLER/api/v1/peers" -H "Authorization: Bearer $ALICE_SESSION"
```

A dev-fallback peer registered with only `X-Tenant-Slug` stays out of
both lists' allowed addresses until it has an owner or a tag the rule
names.

## 5. Leave the relay in the path

The controller's register response includes the relay list. A client
that cannot open a direct WireGuard handshake uses
`ws://<relay-host>/relay` (local compose publishes `ws://127.0.0.1:18443/relay`).
No extra policy step is required for that fallback.

## When there is no OIDC yet

`make local-up` leaves `BAMBOO_REQUIRE_AUTH=false` and
`make local-bootstrap` mints a pre-auth key with no owner. Peers from
that key match `tag:` and `cidr:` only. `./scripts/demo.sh` is that
path. The user and group rules above start working once a real
session mints the key and enrolls the laptop.
