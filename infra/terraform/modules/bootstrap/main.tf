# SPDX-License-Identifier: Apache-2.0
#
# Bootstrap module: creates the S3 bucket + DynamoDB table that hold
# Terraform state for every other module. Run once per AWS account.
# Uses LOCAL state intentionally; the chicken-and-egg of "where does
# the state bucket's state live" is solved by checking the .tfstate
# file into a separate vault out of band.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

variable "account_id" {
  type        = string
  description = "AWS account ID; included in bucket name to keep it globally unique."
}

variable "region" {
  type        = string
  default     = "ap-northeast-1"
  description = "Region for the state bucket. Conventionally the same region as the workloads."
}

provider "aws" {
  region = var.region
}

resource "aws_s3_bucket" "tfstate" {
  bucket        = "bamboo-tfstate-${var.account_id}"
  force_destroy = false
}

resource "aws_s3_bucket_versioning" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  versioning_configuration {
    status = "Enabled"
  }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "tfstate" {
  bucket = aws_s3_bucket.tfstate.id
  rule {
    apply_server_side_encryption_by_default {
      sse_algorithm = "AES256"
    }
  }
}

resource "aws_s3_bucket_public_access_block" "tfstate" {
  bucket                  = aws_s3_bucket.tfstate.id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_dynamodb_table" "tfstate_lock" {
  name         = "bamboo-tfstate-lock"
  billing_mode = "PAY_PER_REQUEST"
  hash_key     = "LockID"

  attribute {
    name = "LockID"
    type = "S"
  }
}

output "bucket" {
  value = aws_s3_bucket.tfstate.id
}

output "lock_table" {
  value = aws_dynamodb_table.tfstate_lock.name
}
