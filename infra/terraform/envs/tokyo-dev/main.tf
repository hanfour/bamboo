# SPDX-License-Identifier: Apache-2.0
#
# tokyo-dev environment composition. Pulls together the modules into
# a runnable Terraform root. Subsequent PRs add database / eks /
# helm modules to this same composition.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
    random = {
      source  = "hashicorp/random"
      version = "~> 3.6"
    }
    clickhouse = {
      source  = "ClickHouse/clickhouse"
      version = "~> 1.0"
    }
  }

  backend "s3" {
    # Filled in via -backend-config=backend.tfvars at init time.
    key            = "envs/tokyo-dev/terraform.tfstate"
    region         = "ap-northeast-1"
    encrypt        = true
    dynamodb_table = "bamboo-tfstate-lock"
  }
}

provider "aws" {
  region = "ap-northeast-1"

  default_tags {
    tags = {
      Project     = "bamboo"
      Environment = "tokyo-dev"
      ManagedBy   = "terraform"
    }
  }
}

variable "domain" {
  type        = string
  description = "Public DNS suffix (e.g. bamboo.example.com). Used by the EKS module's ingress."
}

variable "clickhouse_organization_id" {
  type        = string
  description = "ClickHouse Cloud organization ID. Find under Settings -> API Keys."
  sensitive   = true
}

variable "clickhouse_token_key" {
  type        = string
  description = "ClickHouse Cloud API token key."
  sensitive   = true
}

variable "clickhouse_token_secret" {
  type        = string
  description = "ClickHouse Cloud API token secret."
  sensitive   = true
}

variable "operator_ip_cidrs" {
  type        = list(string)
  description = "CIDRs allowed to query ClickHouse Cloud (operator workstations + the NAT EIP)."
  default     = []
}

provider "clickhouse" {
  organization_id = var.clickhouse_organization_id
  token_key       = var.clickhouse_token_key
  token_secret    = var.clickhouse_token_secret
}

module "network" {
  source = "../../modules/network"
  name   = "tokyo-dev"
}

module "secrets" {
  source = "../../modules/secrets"
  name   = "tokyo-dev"
}

module "database" {
  source             = "../../modules/database"
  name               = "tokyo-dev"
  vpc_id             = module.network.vpc_id
  vpc_cidr           = module.network.vpc_cidr
  private_subnet_ids = module.network.private_subnet_ids
}

module "clickhouse" {
  source       = "../../modules/clickhouse"
  name         = "tokyo-dev"
  region       = "ap-northeast-1"
  ip_allowlist = var.operator_ip_cidrs
}

# Outputs surfaced for ops convenience and for downstream modules.

output "vpc_id" {
  value = module.network.vpc_id
}

output "private_subnet_ids" {
  value = module.network.private_subnet_ids
}

output "public_subnet_ids" {
  value = module.network.public_subnet_ids
}

output "secrets_session_secret_arn" {
  value = module.secrets.session_secret_arn
}

output "postgres_address" {
  value = module.database.postgres_address
}

output "redis_address" {
  value = module.database.redis_address
}

output "clickhouse_endpoint_host" {
  value = module.clickhouse.endpoint_host
}
