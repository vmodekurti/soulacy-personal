resource "aws_kms_key" "credentials" {
  count                   = local.is_multi_user ? 1 : 0
  description             = "Soulacy workspace credential envelopes"
  enable_key_rotation     = true
  deletion_window_in_days = 30
}

resource "aws_kms_alias" "credentials" {
  count         = local.is_multi_user ? 1 : 0
  name          = "alias/${local.prefix}-credentials"
  target_key_id = aws_kms_key.credentials[0].key_id
}

resource "aws_kms_key" "storage" {
  description             = "Soulacy managed storage encryption"
  enable_key_rotation     = true
  deletion_window_in_days = 30
}

resource "aws_kms_alias" "storage" {
  name          = "alias/${local.prefix}-storage"
  target_key_id = aws_kms_key.storage.key_id
}

resource "aws_s3_bucket" "artifacts" {
  count         = local.is_scale ? 1 : 0
  bucket_prefix = "${local.prefix}-artifacts-"
  force_destroy = false
}

resource "aws_s3_bucket_versioning" "artifacts" {
  count  = local.is_scale ? 1 : 0
  bucket = aws_s3_bucket.artifacts[0].id
  versioning_configuration { status = "Enabled" }
}

resource "aws_s3_bucket_server_side_encryption_configuration" "artifacts" {
  count  = local.is_scale ? 1 : 0
  bucket = aws_s3_bucket.artifacts[0].id
  rule {
    apply_server_side_encryption_by_default {
      kms_master_key_id = aws_kms_key.storage.arn
      sse_algorithm     = "aws:kms"
    }
    bucket_key_enabled = true
  }
}

resource "aws_s3_bucket_public_access_block" "artifacts" {
  count                   = local.is_scale ? 1 : 0
  bucket                  = aws_s3_bucket.artifacts[0].id
  block_public_acls       = true
  block_public_policy     = true
  ignore_public_acls      = true
  restrict_public_buckets = true
}

resource "aws_s3_bucket_policy" "artifacts" {
  count  = local.is_scale ? 1 : 0
  bucket = aws_s3_bucket.artifacts[0].id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [{
      Sid       = "DenyInsecureTransport"
      Effect    = "Deny"
      Principal = "*"
      Action    = "s3:*"
      Resource  = [aws_s3_bucket.artifacts[0].arn, "${aws_s3_bucket.artifacts[0].arn}/*"]
      Condition = { Bool = { "aws:SecureTransport" = "false" } }
    }]
  })
}

resource "aws_s3_bucket_lifecycle_configuration" "artifacts" {
  count  = local.is_scale ? 1 : 0
  bucket = aws_s3_bucket.artifacts[0].id
  rule {
    id     = "abort-incomplete-uploads"
    status = "Enabled"
    filter {}
    abort_incomplete_multipart_upload { days_after_initiation = 7 }
    noncurrent_version_expiration { noncurrent_days = 90 }
  }
}

resource "aws_efs_file_system" "workspace" {
  encrypted        = true
  kms_key_id       = aws_kms_key.storage.arn
  performance_mode = "generalPurpose"
  throughput_mode  = "elastic"
  lifecycle_policy { transition_to_ia = "AFTER_30_DAYS" }
  tags = { Name = "${local.prefix}-workspace" }
}

resource "aws_efs_mount_target" "workspace" {
  count           = 2
  file_system_id  = aws_efs_file_system.workspace.id
  subnet_id       = aws_subnet.private[count.index].id
  security_groups = [aws_security_group.efs.id]
}

resource "aws_efs_file_system_policy" "workspace" {
  file_system_id = aws_efs_file_system.workspace.id
  policy = jsonencode({
    Version = "2012-10-17"
    Statement = [
      {
        Sid       = "DenyUnencryptedTransport"
        Effect    = "Deny"
        Principal = "*"
        Action    = "elasticfilesystem:ClientMount"
        Resource  = aws_efs_file_system.workspace.arn
        Condition = { Bool = { "aws:SecureTransport" = "false" } }
      },
      {
        Sid       = "AllowSoulacyRoles"
        Effect    = "Allow"
        Principal = { AWS = concat([aws_iam_role.gateway.arn], local.is_multi_user ? [aws_iam_role.worker[0].arn] : []) }
        Action    = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite", "elasticfilesystem:ClientRootAccess"]
        Resource  = aws_efs_file_system.workspace.arn
        Condition = { Bool = { "aws:SecureTransport" = "true" } }
      }
    ]
  })
}

resource "aws_db_subnet_group" "this" {
  count      = local.is_multi_user ? 1 : 0
  name       = local.prefix
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_db_instance" "postgres" {
  count                         = local.is_multi_user ? 1 : 0
  identifier                    = local.prefix
  engine                        = "postgres"
  engine_version                = "16"
  instance_class                = local.is_budget ? "db.t4g.micro" : var.db_instance_class
  allocated_storage             = local.is_budget ? 20 : 50
  max_allocated_storage         = local.is_budget ? 100 : 500
  storage_type                  = "gp3"
  storage_encrypted             = true
  kms_key_id                    = aws_kms_key.storage.arn
  db_name                       = "soulacy"
  username                      = "soulacy_admin"
  manage_master_user_password   = true
  master_user_secret_kms_key_id = aws_kms_key.storage.arn
  db_subnet_group_name          = aws_db_subnet_group.this[0].name
  vpc_security_group_ids        = [aws_security_group.database.id]
  publicly_accessible           = false
  multi_az                      = !local.is_budget
  backup_retention_period       = local.is_budget ? 1 : 14
  deletion_protection           = local.is_budget ? false : var.enable_deletion_protection
  skip_final_snapshot           = local.is_budget ? true : !var.enable_deletion_protection
  final_snapshot_identifier     = local.is_budget || !var.enable_deletion_protection ? null : "${local.prefix}-final"
  performance_insights_enabled  = !local.is_budget
  auto_minor_version_upgrade    = true
  apply_immediately             = local.is_budget
}

resource "aws_elasticache_subnet_group" "this" {
  count      = local.is_scale ? 1 : 0
  name       = local.prefix
  subnet_ids = aws_subnet.private[*].id
}

resource "aws_elasticache_replication_group" "redis" {
  count                      = local.is_scale ? 1 : 0
  replication_group_id       = local.prefix
  description                = "Soulacy shared rate-limit counter"
  engine                     = "valkey"
  node_type                  = var.redis_node_type
  port                       = 6379
  automatic_failover_enabled = true
  multi_az_enabled           = true
  num_cache_clusters         = 2
  transit_encryption_enabled = true
  at_rest_encryption_enabled = true
  kms_key_id                 = aws_kms_key.storage.arn
  subnet_group_name          = aws_elasticache_subnet_group.this[0].name
  security_group_ids         = [aws_security_group.redis.id]
  snapshot_retention_limit   = 7
  auto_minor_version_upgrade = true
  apply_immediately          = false
}
