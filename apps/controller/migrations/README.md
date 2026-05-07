# Migrations

Database migrations for the controller. Managed by [goose](https://github.com/pressly/goose).

## Conventions

- Filename: `NNNNN_short_description.sql`. Numbers are zero-padded to five digits.
- Each file contains both `Up` and `Down` blocks.
- Use `-- +goose StatementBegin / StatementEnd` for multi-statement DDL so
  goose runs them in a single transaction.

## Local usage

```bash
# install goose (one-off)
make bootstrap

# run all up migrations
goose -dir apps/controller/migrations postgres "$DATABASE_URL" up

# rollback the most recent
goose -dir apps/controller/migrations postgres "$DATABASE_URL" down

# show status
goose -dir apps/controller/migrations postgres "$DATABASE_URL" status
```

The local dev stack (`make dev`) provides Postgres at
`postgres://bamboo:dev@localhost:15432/bamboo?sslmode=disable` (port 15432
chosen to avoid conflicts with system Postgres on the standard 5432).

## Schema overview (v1)

| Table                | Purpose                                          |
| -------------------- | ------------------------------------------------ |
| `tenants`            | Multi-tenancy root; allocates a CIDR from 100.64.0.0/10 |
| `users`              | OIDC-authenticated humans                        |
| `user_groups`        | Named groups (referenced by ACL `group:` matchers) |
| `user_group_members` | Membership join                                  |
| `peers`              | Devices on the mesh; one row per WireGuard key   |
| `tags`               | Labels for peers (referenced by ACL `tag:` matchers) |
| `peer_tags`          | Tag assignment join                              |
| `acl_policies`       | One row per tenant (current policy)              |
| `acl_policy_history` | Append-only revisions for diff / rollback        |
| `pre_auth_keys`      | Headless / CI registration tokens                |
| `audit_log`          | Append-only operation log                        |

## Multi-tenancy

Every tenant-scoped table has a `tenant_id` column with:
- Foreign key to `tenants(id)` with `ON DELETE CASCADE`
- An index on `tenant_id`
- A composite uniqueness constraint where applicable

There is no row-level security policy in v1; isolation is enforced in
application code. Future ADR may revisit RLS once we have measured the
performance cost.

## Tracking

- [Sprint 1 — Issue #5](https://github.com/hanfour/bamboo/issues/5)
