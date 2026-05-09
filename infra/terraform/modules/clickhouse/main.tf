# SPDX-License-Identifier: Apache-2.0
#
# ClickHouse module: provisions a ClickHouse Cloud development
# service. ClickHouse Cloud is operated by ClickHouse Inc; we use
# their official Terraform provider rather than self-hosting because
# the operational overhead of a sharded ClickHouse deployment is
# wildly disproportionate to bamboo's telemetry volume in early
# stages.
#
# Cost: ~$66/mo for the development tier. Upgrade to production when
# we have actual telemetry pressure.

terraform {
  required_version = ">= 1.6"
  required_providers {
    clickhouse = {
      source  = "ClickHouse/clickhouse"
      version = "~> 1.0"
    }
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

variable "name" {
  type        = string
  description = "Service name in ClickHouse Cloud (e.g. tokyo-dev)."
}

variable "region" {
  type        = string
  default     = "ap-northeast-1"
  description = "ClickHouse Cloud region. Tokyo aligns with the rest of the stack."
}

variable "tier" {
  type    = string
  default = "development"
}

variable "ip_allowlist" {
  type        = list(string)
  description = "CIDRs allowed to reach the ClickHouse Cloud service. The EKS NAT Gateway EIP and operator IPs are typical entries."
  default     = []
}

variable "tags" {
  type    = map(string)
  default = {}
}

resource "clickhouse_service" "this" {
  name           = var.name
  cloud_provider = "aws"
  region         = var.region
  tier           = var.tier

  # Allowlist: deny by default; the EKS NAT EIP and operator
  # CIDRs are passed in. ClickHouse Cloud has a public endpoint
  # (TLS, password-auth) so this is the only network control.
  ip_access = [
    for cidr in var.ip_allowlist : {
      source      = cidr
      description = "bamboo allowlist"
    }
  ]

  # Tier-specific knobs. The development tier auto-suspends after
  # 15 minutes idle; bring it up by sending a query.
  min_total_memory_gb = 8
  max_total_memory_gb = 16

  idle_scaling      = true
  idle_timeout_minutes = 15
}

resource "aws_secretsmanager_secret" "clickhouse_url" {
  name        = "${var.name}/bamboo/clickhouse_url"
  description = "ClickHouse Cloud DSN for the bamboo controller."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "clickhouse_url" {
  secret_id = aws_secretsmanager_secret.clickhouse_url.id
  secret_string = format(
    "https://%s:%s@%s:%d/default?secure=1",
    clickhouse_service.this.endpoints[0].username,
    clickhouse_service.this.endpoints[0].password,
    clickhouse_service.this.endpoints[0].host,
    clickhouse_service.this.endpoints[0].port,
  )
}

output "service_id" {
  value = clickhouse_service.this.id
}

output "endpoint_host" {
  value = clickhouse_service.this.endpoints[0].host
}

output "url_secret_arn" {
  value     = aws_secretsmanager_secret.clickhouse_url.arn
  sensitive = true
}
