# SPDX-License-Identifier: Apache-2.0
#
# Network module: VPC, three public + three private subnets across
# three AZs, an Internet Gateway, and a single shared NAT Gateway.
# Multi-AZ NAT (one per zone) is the production standard but costs
# ~3x more in NAT-hours; we start with one and document the upgrade
# path. EKS + RDS care that the subnets exist + are tagged correctly,
# not how many NAT GWs sit upstream.

terraform {
  required_version = ">= 1.6"
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 5.0"
    }
  }
}

variable "name" {
  type        = string
  description = "Environment / VPC name. Becomes a tag and a name prefix."
}

variable "cidr" {
  type        = string
  default     = "10.40.0.0/16"
  description = "VPC CIDR. /16 leaves room for /20 subnets per AZ."
}

variable "az_count" {
  type        = number
  default     = 3
  description = "How many AZs to spread subnets across. EKS+RDS recommend 3."
}

variable "tags" {
  type    = map(string)
  default = {}
}

data "aws_availability_zones" "available" {
  state = "available"
}

locals {
  azs = slice(data.aws_availability_zones.available.names, 0, var.az_count)
  base_tags = merge({
    "Project"     = "bamboo"
    "ManagedBy"   = "terraform"
    "Environment" = var.name
  }, var.tags)
}

resource "aws_vpc" "this" {
  cidr_block           = var.cidr
  enable_dns_hostnames = true
  enable_dns_support   = true
  tags                 = merge(local.base_tags, { "Name" = "${var.name}-vpc" })
}

resource "aws_internet_gateway" "this" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.base_tags, { "Name" = "${var.name}-igw" })
}

# -----------------------------------------------------------------------------
# Public subnets — receive ALB / NAT GW.
# -----------------------------------------------------------------------------

resource "aws_subnet" "public" {
  count                   = length(local.azs)
  vpc_id                  = aws_vpc.this.id
  availability_zone       = local.azs[count.index]
  cidr_block              = cidrsubnet(var.cidr, 4, count.index)
  map_public_ip_on_launch = true

  tags = merge(local.base_tags, {
    "Name"                                    = "${var.name}-public-${local.azs[count.index]}"
    "kubernetes.io/role/elb"                  = "1"
    "kubernetes.io/cluster/${var.name}-eks"   = "shared"
  })
}

resource "aws_route_table" "public" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.base_tags, { "Name" = "${var.name}-public-rt" })
}

resource "aws_route" "public_default" {
  route_table_id         = aws_route_table.public.id
  destination_cidr_block = "0.0.0.0/0"
  gateway_id             = aws_internet_gateway.this.id
}

resource "aws_route_table_association" "public" {
  count          = length(local.azs)
  subnet_id      = aws_subnet.public[count.index].id
  route_table_id = aws_route_table.public.id
}

# -----------------------------------------------------------------------------
# Private subnets — workloads + RDS + ElastiCache + EKS nodes.
# -----------------------------------------------------------------------------

resource "aws_subnet" "private" {
  count             = length(local.azs)
  vpc_id            = aws_vpc.this.id
  availability_zone = local.azs[count.index]
  cidr_block        = cidrsubnet(var.cidr, 4, count.index + length(local.azs))

  tags = merge(local.base_tags, {
    "Name"                                       = "${var.name}-private-${local.azs[count.index]}"
    "kubernetes.io/role/internal-elb"            = "1"
    "kubernetes.io/cluster/${var.name}-eks"      = "shared"
  })
}

# Single shared NAT GW — sits in the first public subnet. The
# trade-off is documented at the top of this file.
resource "aws_eip" "nat" {
  domain = "vpc"
  tags   = merge(local.base_tags, { "Name" = "${var.name}-nat-eip" })
}

resource "aws_nat_gateway" "this" {
  allocation_id = aws_eip.nat.id
  subnet_id     = aws_subnet.public[0].id
  tags          = merge(local.base_tags, { "Name" = "${var.name}-nat" })
  depends_on    = [aws_internet_gateway.this]
}

resource "aws_route_table" "private" {
  vpc_id = aws_vpc.this.id
  tags   = merge(local.base_tags, { "Name" = "${var.name}-private-rt" })
}

resource "aws_route" "private_default" {
  route_table_id         = aws_route_table.private.id
  destination_cidr_block = "0.0.0.0/0"
  nat_gateway_id         = aws_nat_gateway.this.id
}

resource "aws_route_table_association" "private" {
  count          = length(local.azs)
  subnet_id      = aws_subnet.private[count.index].id
  route_table_id = aws_route_table.private.id
}

# -----------------------------------------------------------------------------
# Outputs consumed by database / eks modules.
# -----------------------------------------------------------------------------

output "vpc_id" {
  value = aws_vpc.this.id
}

output "vpc_cidr" {
  value = aws_vpc.this.cidr_block
}

output "public_subnet_ids" {
  value = aws_subnet.public[*].id
}

output "private_subnet_ids" {
  value = aws_subnet.private[*].id
}

output "azs" {
  value = local.azs
}
