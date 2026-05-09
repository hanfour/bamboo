# infra/terraform

Terraform modules for deploying bamboo. Per
[ADR 0009](../../docs/adr/0009-cloud-provider-strategy.md) the
production target is AWS Tokyo (`ap-northeast-1`) with Cloudflare
front-loading and Vultr / Hetzner relays.

**License:** Apache 2.0 — see [LICENSE-APACHE](../../LICENSE-APACHE).

## Layout

```
infra/terraform/
  modules/                  reusable building blocks
    bootstrap/                state bucket + DynamoDB lock table
    network/                  VPC, subnets, NAT, security groups
    secrets/                  Secrets Manager entries
    database/                 RDS Postgres + ElastiCache Redis     (RRRR)
    eks/                      EKS cluster + node group             (SSSS)
  envs/                     concrete environments (compose modules)
    tokyo-dev/                primary dev environment, ap-northeast-1
```

## Bootstrap once per AWS account

The Terraform state lives in an S3 bucket with a DynamoDB lock
table. The bucket itself has to come from somewhere; we create it
with the `bootstrap` module using local state, then never touch it
again.

```bash
cd modules/bootstrap
terraform init
terraform apply -var=account_id=$(aws sts get-caller-identity --query Account --output text)
# -> outputs the bucket and lock-table names
```

Copy the bucket name into `envs/tokyo-dev/backend.tfvars` (the
`backend "s3"` block in `envs/*/main.tf` reads it at init time).

## Bring up tokyo-dev

```bash
cd envs/tokyo-dev
cp backend.tfvars.example backend.tfvars
cp terraform.tfvars.example terraform.tfvars
$EDITOR backend.tfvars terraform.tfvars
terraform init -backend-config=backend.tfvars
terraform plan -out=tfplan
terraform apply tfplan
```

This currently provisions: VPC + Secrets Manager. The rest of the
stack (RDS, EKS, Helm releases) lands in subsequent PRs (RRRR/SSSS).

## After applying

The OIDC client_secret slots in Secrets Manager start empty. After
registering OAuth clients in the Google / GitHub consoles, edit the
two secrets directly in the AWS console (terraform's lifecycle
ignore_changes leaves them alone on subsequent applies):

- `tokyo-dev/bamboo/oidc/google` — `{"client_id":"...","client_secret":"..."}`
- `tokyo-dev/bamboo/oidc/github` — same shape

The session_secret is auto-generated on first apply and rotated only
when you `terraform taint` it explicitly.

## Cost expectations (when fully populated)

| Component        | Monthly minimum |
| ---------------- | ---------------- |
| EKS control plane | $72              |
| 2x t3.small spot nodes | $20         |
| RDS db.t4g.micro Multi-AZ | $30      |
| ElastiCache cache.t4g.micro | $15    |
| ClickHouse Cloud Development | $66   |
| NAT Gateway (ap-northeast-1) | $33   |
| ALB                | $20             |
| **Floor**        | **~$256/mo**     |

Numbers are approximate; review `terraform plan` output for the
actual mix. Cost can be cut by ~$50/mo by skipping Multi-AZ on the
RDS and using a single NAT Gateway shared across AZs (already the
default in this module).
