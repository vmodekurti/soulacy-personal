data "aws_ssm_parameter" "ubuntu_ami" {
  name = "/aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id"
}

locals {
  ecr_registry = "${data.aws_caller_identity.current.account_id}.dkr.ecr.${var.aws_region}.amazonaws.com"
  common_bootstrap = templatefile("${path.module}/templates/common.sh.tftpl", {
    aws_region     = var.aws_region
    ecr_registry   = local.ecr_registry
    gateway_image  = var.gateway_image
    cosign_version = var.cosign_version
  })
}

resource "aws_instance" "nats" {
  count                       = local.is_multi_user ? 1 : 0
  ami                         = data.aws_ssm_parameter.ubuntu_ami.value
  instance_type               = var.nats_instance_type
  subnet_id                   = aws_subnet.private[0].id
  vpc_security_group_ids      = [aws_security_group.nats.id]
  iam_instance_profile        = aws_iam_instance_profile.nats[0].name
  user_data_replace_on_change = true
  user_data = templatefile("${path.module}/templates/nats-user-data.sh.tftpl", {
    aws_region      = var.aws_region
    nats_secret_arn = data.aws_secretsmanager_secret.nats_tls[0].arn
    nats_image      = var.nats_image
  })

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }
  root_block_device {
    encrypted   = true
    kms_key_id  = aws_kms_key.storage.arn
    volume_type = "gp3"
    volume_size = 40
  }
  tags = { Name = "${local.prefix}-nats" }
}

resource "aws_route53_record" "nats" {
  count   = local.is_multi_user ? 1 : 0
  zone_id = aws_route53_zone.internal.zone_id
  name    = "nats.soulacy.internal"
  type    = "A"
  ttl     = 30
  records = [aws_instance.nats[0].private_ip]
}

resource "aws_instance" "worker" {
  count                       = local.is_multi_user ? 1 : 0
  ami                         = data.aws_ssm_parameter.ubuntu_ami.value
  instance_type               = var.worker_instance_type
  subnet_id                   = aws_subnet.private[1].id
  vpc_security_group_ids      = [aws_security_group.worker.id]
  iam_instance_profile        = aws_iam_instance_profile.worker[0].name
  user_data_replace_on_change = true
  user_data = templatefile("${path.module}/templates/worker-user-data.sh.tftpl", {
    common               = local.common_bootstrap
    aws_region           = var.aws_region
    bootstrap_secret_arn = data.aws_secretsmanager_secret.bootstrap.arn
    nats_secret_arn      = data.aws_secretsmanager_secret.nats_tls[0].arn
    execution_image      = var.execution_image
    worker_concurrency   = var.worker_concurrency
  })

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }
  root_block_device {
    encrypted   = true
    kms_key_id  = aws_kms_key.storage.arn
    volume_type = "gp3"
    volume_size = 50
  }
  depends_on = [aws_route53_record.nats]
  tags       = { Name = "${local.prefix}-worker" }
}

resource "aws_instance" "gateway" {
  ami                         = data.aws_ssm_parameter.ubuntu_ami.value
  instance_type               = var.gateway_instance_type
  subnet_id                   = aws_subnet.private[0].id
  vpc_security_group_ids      = [aws_security_group.gateway.id]
  iam_instance_profile        = aws_iam_instance_profile.gateway.name
  user_data_replace_on_change = true
  user_data = local.is_multi_user ? templatefile("${path.module}/templates/gateway-user-data.sh.tftpl", {
    common               = local.common_bootstrap
    aws_region           = var.aws_region
    deployment_mode      = var.deployment_mode
    is_scale             = local.is_scale
    domain_name          = var.domain_name
    oidc_issuer          = var.oidc_issuer
    oidc_client_id       = var.oidc_client_id
    efs_id               = aws_efs_file_system.workspace.id
    bootstrap_secret_arn = data.aws_secretsmanager_secret.bootstrap.arn
    nats_secret_arn      = data.aws_secretsmanager_secret.nats_tls[0].arn
    rds_secret_arn       = aws_db_instance.postgres[0].master_user_secret[0].secret_arn
    db_host              = aws_db_instance.postgres[0].address
    redis_host           = local.is_scale ? aws_elasticache_replication_group.redis[0].primary_endpoint_address : ""
    artifact_bucket      = local.is_scale ? aws_s3_bucket.artifacts[0].id : ""
    credential_kms_arn   = aws_kms_key.credentials[0].arn
    execution_image      = var.execution_image
    }) : templatefile("${path.module}/templates/personal-gateway-user-data.sh.tftpl", {
    common               = local.common_bootstrap
    aws_region           = var.aws_region
    domain_name          = var.domain_name
    efs_id               = aws_efs_file_system.workspace.id
    bootstrap_secret_arn = data.aws_secretsmanager_secret.bootstrap.arn
    execution_image      = var.execution_image
  })

  metadata_options {
    http_endpoint = "enabled"
    http_tokens   = "required"
  }
  root_block_device {
    encrypted   = true
    kms_key_id  = aws_kms_key.storage.arn
    volume_type = "gp3"
    volume_size = 40
  }
  depends_on = [aws_efs_mount_target.workspace]
  tags       = { Name = "${local.prefix}-gateway" }
}

resource "aws_lb" "this" {
  name                       = substr(local.prefix, 0, 32)
  internal                   = false
  load_balancer_type         = "application"
  security_groups            = [aws_security_group.alb.id]
  subnets                    = aws_subnet.public[*].id
  drop_invalid_header_fields = true
}

resource "aws_lb_target_group" "gateway" {
  name        = substr("${local.prefix}-gateway", 0, 32)
  port        = 1947
  protocol    = "HTTP"
  vpc_id      = aws_vpc.this.id
  target_type = "instance"

  health_check {
    enabled             = true
    # Public, code-only readiness endpoint. /api/v1/health is intentionally
    # authenticated and cannot be used by an ALB health checker.
    path                = "/ready"
    protocol            = "HTTP"
    matcher             = "200"
    interval            = 15
    timeout             = 5
    healthy_threshold   = 2
    unhealthy_threshold = 4
  }
}

resource "aws_lb_target_group_attachment" "gateway" {
  target_group_arn = aws_lb_target_group.gateway.arn
  target_id        = aws_instance.gateway.id
  port             = 1947
}

resource "aws_lb_listener" "https" {
  load_balancer_arn = aws_lb.this.arn
  port              = 443
  protocol          = "HTTPS"
  ssl_policy        = "ELBSecurityPolicy-TLS13-1-2-2021-06"
  certificate_arn   = aws_acm_certificate_validation.this.certificate_arn

  default_action {
    type             = "forward"
    target_group_arn = aws_lb_target_group.gateway.arn
  }
}

resource "aws_route53_record" "application" {
  zone_id = var.route53_zone_id
  name    = var.domain_name
  type    = "A"
  alias {
    name                   = aws_lb.this.dns_name
    zone_id                = aws_lb.this.zone_id
    evaluate_target_health = true
  }
}
