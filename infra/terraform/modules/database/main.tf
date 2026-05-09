# SPDX-License-Identifier: Apache-2.0
#
# Database module: RDS Postgres + ElastiCache Redis. Both sit in the
# private subnets and are reachable only from inside the VPC.
# Postgres is the source of truth for tenants/users/peers/policies;
# Redis is reserved for ephemeral state (session cache, rate limit
# counters) — currently unused by the controller but provisioned now
# so the EKS module can wire DATABASE_URL + REDIS_URL atomically.

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
  description = "Environment name; used as a name prefix."
}

variable "vpc_id" {
  type = string
}

variable "vpc_cidr" {
  type        = string
  description = "VPC CIDR; used to size the security-group ingress."
}

variable "private_subnet_ids" {
  type = list(string)
}

variable "postgres_instance_class" {
  type        = string
  default     = "db.t4g.micro"
  description = "RDS instance class. db.t4g.micro is sufficient for first-user dogfood."
}

variable "postgres_storage_gb" {
  type        = number
  default     = 20
  description = "Allocated storage. RDS auto-grows up to max_allocated_storage."
}

variable "postgres_max_storage_gb" {
  type    = number
  default = 100
}

variable "postgres_multi_az" {
  type        = bool
  default     = false
  description = "Multi-AZ doubles cost. False is fine for tokyo-dev; production should set true."
}

variable "redis_node_type" {
  type    = string
  default = "cache.t4g.micro"
}

variable "tags" {
  type    = map(string)
  default = {}
}

# -----------------------------------------------------------------------------
# Security groups: one for Postgres, one for Redis. Only allow access
# from inside the VPC. The EKS module's worker security-group ID can
# be added as an additional ingress source via the *_extra_sg_ids
# variables.
# -----------------------------------------------------------------------------

resource "aws_security_group" "postgres" {
  name_prefix = "${var.name}-postgres-"
  description = "Postgres ingress from VPC."
  vpc_id      = var.vpc_id

  ingress {
    description = "Postgres from VPC"
    from_port   = 5432
    to_port     = 5432
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(var.tags, { "Name" = "${var.name}-postgres-sg" })
}

resource "aws_security_group" "redis" {
  name_prefix = "${var.name}-redis-"
  description = "Redis ingress from VPC."
  vpc_id      = var.vpc_id

  ingress {
    description = "Redis from VPC"
    from_port   = 6379
    to_port     = 6379
    protocol    = "tcp"
    cidr_blocks = [var.vpc_cidr]
  }

  egress {
    from_port   = 0
    to_port     = 0
    protocol    = "-1"
    cidr_blocks = ["0.0.0.0/0"]
  }

  tags = merge(var.tags, { "Name" = "${var.name}-redis-sg" })
}

# -----------------------------------------------------------------------------
# Postgres
# -----------------------------------------------------------------------------

resource "aws_db_subnet_group" "postgres" {
  name       = "${var.name}-postgres"
  subnet_ids = var.private_subnet_ids
  tags       = var.tags
}

resource "random_password" "postgres" {
  length  = 32
  special = false
}

resource "aws_db_instance" "postgres" {
  identifier             = "${var.name}-postgres"
  engine                 = "postgres"
  engine_version         = "16.4"
  instance_class         = var.postgres_instance_class
  allocated_storage      = var.postgres_storage_gb
  max_allocated_storage  = var.postgres_max_storage_gb
  storage_type           = "gp3"
  storage_encrypted      = true

  db_name  = "bamboo"
  username = "bamboo"
  password = random_password.postgres.result

  multi_az               = var.postgres_multi_az
  publicly_accessible    = false
  vpc_security_group_ids = [aws_security_group.postgres.id]
  db_subnet_group_name   = aws_db_subnet_group.postgres.name

  backup_retention_period = 7
  backup_window           = "17:00-18:00" # 02:00-03:00 JST
  maintenance_window      = "Sun:18:00-Sun:19:00"

  skip_final_snapshot = true # tokyo-dev only; production sets false
  deletion_protection = false

  tags = merge(var.tags, { "Name" = "${var.name}-postgres" })
}

resource "aws_secretsmanager_secret" "postgres_url" {
  name        = "${var.name}/bamboo/database_url"
  description = "Postgres connection URL for the bamboo controller."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "postgres_url" {
  secret_id = aws_secretsmanager_secret.postgres_url.id
  secret_string = format(
    "postgres://%s:%s@%s:%d/%s?sslmode=require",
    "bamboo",
    random_password.postgres.result,
    aws_db_instance.postgres.address,
    aws_db_instance.postgres.port,
    "bamboo",
  )
}

# -----------------------------------------------------------------------------
# Redis (ElastiCache)
# -----------------------------------------------------------------------------

resource "aws_elasticache_subnet_group" "redis" {
  name       = "${var.name}-redis"
  subnet_ids = var.private_subnet_ids
  tags       = var.tags
}

resource "aws_elasticache_cluster" "redis" {
  cluster_id           = "${var.name}-redis"
  engine               = "redis"
  engine_version       = "7.1"
  node_type            = var.redis_node_type
  num_cache_nodes      = 1
  parameter_group_name = "default.redis7"
  subnet_group_name    = aws_elasticache_subnet_group.redis.name
  security_group_ids   = [aws_security_group.redis.id]
  port                 = 6379

  tags = merge(var.tags, { "Name" = "${var.name}-redis" })
}

resource "aws_secretsmanager_secret" "redis_url" {
  name        = "${var.name}/bamboo/redis_url"
  description = "Redis connection URL for the bamboo controller."
  tags        = var.tags
}

resource "aws_secretsmanager_secret_version" "redis_url" {
  secret_id = aws_secretsmanager_secret.redis_url.id
  secret_string = format(
    "redis://%s:%d",
    aws_elasticache_cluster.redis.cache_nodes[0].address,
    aws_elasticache_cluster.redis.cache_nodes[0].port,
  )
}

# -----------------------------------------------------------------------------
# Outputs
# -----------------------------------------------------------------------------

output "postgres_address" {
  value = aws_db_instance.postgres.address
}

output "postgres_url_secret_arn" {
  value     = aws_secretsmanager_secret.postgres_url.arn
  sensitive = true
}

output "redis_address" {
  value = aws_elasticache_cluster.redis.cache_nodes[0].address
}

output "redis_url_secret_arn" {
  value     = aws_secretsmanager_secret.redis_url.arn
  sensitive = true
}

output "postgres_security_group_id" {
  value = aws_security_group.postgres.id
}

output "redis_security_group_id" {
  value = aws_security_group.redis.id
}
