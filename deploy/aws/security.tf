resource "aws_security_group" "alb" {
  name_prefix = "${local.prefix}-alb-"
  description = "Public HTTPS ingress"
  vpc_id      = aws_vpc.this.id

  ingress {
    description      = "HTTPS"
    protocol         = "tcp"
    from_port        = 443
    to_port          = 443
    cidr_blocks      = ["0.0.0.0/0"]
    ipv6_cidr_blocks = ["::/0"]
  }
  egress {
    protocol    = "tcp"
    from_port   = 1947
    to_port     = 1947
    cidr_blocks = [var.vpc_cidr]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_security_group" "gateway" {
  name_prefix = "${local.prefix}-gateway-"
  description = "Soulacy gateway"
  vpc_id      = aws_vpc.this.id

  ingress {
    description     = "ALB to gateway"
    protocol        = "tcp"
    from_port       = 1947
    to_port         = 1947
    security_groups = [aws_security_group.alb.id]
  }
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_security_group" "worker" {
  name_prefix = "${local.prefix}-worker-"
  description = "Soulacy isolated worker; no inbound application ports"
  vpc_id      = aws_vpc.this.id
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_security_group" "nats" {
  name_prefix = "${local.prefix}-nats-"
  description = "mTLS NATS JetStream"
  vpc_id      = aws_vpc.this.id

  ingress {
    description     = "Gateway NATS client"
    protocol        = "tcp"
    from_port       = 4222
    to_port         = 4222
    security_groups = [aws_security_group.gateway.id]
  }
  ingress {
    description     = "Worker NATS client"
    protocol        = "tcp"
    from_port       = 4222
    to_port         = 4222
    security_groups = [aws_security_group.worker.id]
  }
  egress {
    protocol    = "-1"
    from_port   = 0
    to_port     = 0
    cidr_blocks = ["0.0.0.0/0"]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_security_group" "database" {
  name_prefix = "${local.prefix}-db-"
  description = "PostgreSQL from gateway only"
  vpc_id      = aws_vpc.this.id
  ingress {
    protocol        = "tcp"
    from_port       = 5432
    to_port         = 5432
    security_groups = [aws_security_group.gateway.id]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_security_group" "redis" {
  name_prefix = "${local.prefix}-redis-"
  description = "TLS Redis/Valkey from gateway only"
  vpc_id      = aws_vpc.this.id
  ingress {
    protocol        = "tcp"
    from_port       = 6379
    to_port         = 6379
    security_groups = [aws_security_group.gateway.id]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_security_group" "efs" {
  name_prefix = "${local.prefix}-efs-"
  description = "EFS from gateway only"
  vpc_id      = aws_vpc.this.id
  ingress {
    protocol        = "tcp"
    from_port       = 2049
    to_port         = 2049
    security_groups = [aws_security_group.gateway.id]
  }
  lifecycle { create_before_destroy = true }
}

resource "aws_acm_certificate" "this" {
  domain_name       = var.domain_name
  validation_method = "DNS"
  lifecycle { create_before_destroy = true }
}

resource "aws_route53_record" "certificate_validation" {
  count   = 1
  zone_id = var.route53_zone_id
  name    = tolist(aws_acm_certificate.this.domain_validation_options)[0].resource_record_name
  type    = tolist(aws_acm_certificate.this.domain_validation_options)[0].resource_record_type
  ttl     = 60
  records = [tolist(aws_acm_certificate.this.domain_validation_options)[0].resource_record_value]
}

resource "aws_acm_certificate_validation" "this" {
  certificate_arn         = aws_acm_certificate.this.arn
  validation_record_fqdns = [aws_route53_record.certificate_validation[0].fqdn]
}
