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

module "network" {
  source = "../../modules/network"
  name   = "tokyo-dev"
}

module "secrets" {
  source = "../../modules/secrets"
  name   = "tokyo-dev"
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
