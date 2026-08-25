data "aws_caller_identity" "current" {}
data "aws_partition" "current" {}

data "aws_secretsmanager_secret" "bootstrap" {
  name = var.bootstrap_secret_name
}

data "aws_secretsmanager_secret" "nats_tls" {
  count = local.is_multi_user ? 1 : 0
  name  = var.nats_tls_secret_name
}

data "aws_iam_policy_document" "ec2_assume" {
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["ec2.amazonaws.com"]
    }
  }
}

resource "aws_iam_role" "gateway" {
  name_prefix        = "${local.prefix}-gateway-"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_role_policy_attachment" "gateway_ssm" {
  role       = aws_iam_role.gateway.name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "gateway" {
  statement {
    sid     = "ReadBootstrapSecrets"
    actions = ["secretsmanager:GetSecretValue"]
    resources = concat(
      [data.aws_secretsmanager_secret.bootstrap.arn],
      local.is_multi_user ? [data.aws_secretsmanager_secret.nats_tls[0].arn, aws_db_instance.postgres[0].master_user_secret[0].secret_arn] : []
    )
  }
  statement {
    sid       = "WorkspaceCredentialKMS"
    actions   = ["kms:Encrypt", "kms:Decrypt", "kms:GenerateDataKey", "kms:DescribeKey"]
    resources = concat([aws_kms_key.storage.arn], local.is_multi_user ? [aws_kms_key.credentials[0].arn] : [])
  }
  dynamic "statement" {
    for_each = local.is_scale ? [1] : []
    content {
      sid       = "Artifacts"
      actions   = ["s3:GetObject", "s3:PutObject", "s3:DeleteObject", "s3:ListBucket"]
      resources = [aws_s3_bucket.artifacts[0].arn, "${aws_s3_bucket.artifacts[0].arn}/*"]
    }
  }
  statement {
    sid       = "WorkspaceFilesystem"
    actions   = ["elasticfilesystem:ClientMount", "elasticfilesystem:ClientWrite", "elasticfilesystem:ClientRootAccess"]
    resources = [aws_efs_file_system.workspace.arn]
  }
  statement {
    sid       = "ECRToken"
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    sid       = "ECRPull"
    actions   = ["ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"]
    resources = ["arn:${data.aws_partition.current.partition}:ecr:${var.aws_region}:${data.aws_caller_identity.current.account_id}:repository/${var.name}*"]
  }
}

resource "aws_iam_role_policy" "gateway" {
  role   = aws_iam_role.gateway.id
  policy = data.aws_iam_policy_document.gateway.json
}

resource "aws_iam_instance_profile" "gateway" {
  name_prefix = "${local.prefix}-gateway-"
  role        = aws_iam_role.gateway.name
}

resource "aws_iam_role" "worker" {
  count              = local.is_multi_user ? 1 : 0
  name_prefix        = "${local.prefix}-worker-"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_role_policy_attachment" "worker_ssm" {
  count      = local.is_multi_user ? 1 : 0
  role       = aws_iam_role.worker[0].name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

data "aws_iam_policy_document" "worker" {
  count = local.is_multi_user ? 1 : 0
  statement {
    actions   = ["secretsmanager:GetSecretValue"]
    resources = [data.aws_secretsmanager_secret.bootstrap.arn, data.aws_secretsmanager_secret.nats_tls[0].arn]
  }
  statement {
    actions   = ["ecr:GetAuthorizationToken"]
    resources = ["*"]
  }
  statement {
    actions   = ["ecr:BatchCheckLayerAvailability", "ecr:GetDownloadUrlForLayer", "ecr:BatchGetImage"]
    resources = ["arn:${data.aws_partition.current.partition}:ecr:${var.aws_region}:${data.aws_caller_identity.current.account_id}:repository/${var.name}*"]
  }
}

resource "aws_iam_role_policy" "worker" {
  count  = local.is_multi_user ? 1 : 0
  role   = aws_iam_role.worker[0].id
  policy = data.aws_iam_policy_document.worker[0].json
}

resource "aws_iam_instance_profile" "worker" {
  count       = local.is_multi_user ? 1 : 0
  name_prefix = "${local.prefix}-worker-"
  role        = aws_iam_role.worker[0].name
}

resource "aws_iam_role" "nats" {
  count              = local.is_multi_user ? 1 : 0
  name_prefix        = "${local.prefix}-nats-"
  assume_role_policy = data.aws_iam_policy_document.ec2_assume.json
}

resource "aws_iam_role_policy_attachment" "nats_ssm" {
  count      = local.is_multi_user ? 1 : 0
  role       = aws_iam_role.nats[0].name
  policy_arn = "arn:${data.aws_partition.current.partition}:iam::aws:policy/AmazonSSMManagedInstanceCore"
}

resource "aws_iam_role_policy" "nats" {
  count = local.is_multi_user ? 1 : 0
  role  = aws_iam_role.nats[0].id
  policy = jsonencode({
    Version   = "2012-10-17"
    Statement = [{ Effect = "Allow", Action = "secretsmanager:GetSecretValue", Resource = data.aws_secretsmanager_secret.nats_tls[0].arn }]
  })
}

resource "aws_iam_instance_profile" "nats" {
  count       = local.is_multi_user ? 1 : 0
  name_prefix = "${local.prefix}-nats-"
  role        = aws_iam_role.nats[0].name
}
