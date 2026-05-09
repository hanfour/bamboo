# SPDX-License-Identifier: Apache-2.0
#
# Secrets module: AWS Secrets Manager entries the bamboo controller
# reads at boot. Three categories:
#
#   session_secret        HMAC key for issuing OIDC session JWTs +
#                         relay tokens. 64 random bytes; rotated by
#                         updating this resource and re-rolling the
#                         controller (existing tokens invalidate).
#   oidc_google           Google OAuth client_id + client_secret JSON.
#                         Empty by default; populate after registering
#                         the OAuth client in console.cloud.google.com.
#   oidc_github           Same shape, GitHub.

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
}

variable "name" {
  type        = string
  description = "Environment name (becomes a Secret name prefix)."
}

variable "tags" {
  type    = map(string)
  default = {}
}

resource "random_bytes" "session_secret" {
  length = 64
}

resource "aws_secretsmanager_secret" "session_secret" {
  name        = "${var.name}/bamboo/session_secret"
  description = "HMAC key for bamboo controller session + relay JWTs."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "session_secret" {
  secret_id     = aws_secretsmanager_secret.session_secret.id
  secret_string = random_bytes.session_secret.base64
}

resource "aws_secretsmanager_secret" "oidc_google" {
  name        = "${var.name}/bamboo/oidc/google"
  description = "Google OIDC client_id + client_secret JSON. Populate manually after creating the OAuth client."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "oidc_google_init" {
  secret_id = aws_secretsmanager_secret.oidc_google.id
  secret_string = jsonencode({
    client_id     = ""
    client_secret = ""
  })
  lifecycle {
    # Operators edit this secret in the AWS console once they have
    # real OAuth credentials. Don't fight them on every apply.
    ignore_changes = [secret_string]
  }
}

resource "aws_secretsmanager_secret" "oidc_github" {
  name        = "${var.name}/bamboo/oidc/github"
  description = "GitHub OIDC client_id + client_secret JSON. Populate manually after creating the OAuth app."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "oidc_github_init" {
  secret_id = aws_secretsmanager_secret.oidc_github.id
  secret_string = jsonencode({
    client_id     = ""
    client_secret = ""
  })
  lifecycle {
    ignore_changes = [secret_string]
  }
}

output "session_secret_arn" {
  value = aws_secretsmanager_secret.session_secret.arn
}

output "oidc_google_arn" {
  value = aws_secretsmanager_secret.oidc_google.arn
}

output "oidc_github_arn" {
  value = aws_secretsmanager_secret.oidc_github.arn
}
