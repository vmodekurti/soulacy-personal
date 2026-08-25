variable "aws_region" {
  description = "AWS region for the deployment."
  type        = string
  default     = "us-east-1"
}

variable "name" {
  description = "Short deployment name used in resource names."
  type        = string
  default     = "soulacy"
  validation {
    condition     = can(regex("^[a-z][a-z0-9-]{1,20}$", var.name))
    error_message = "name must be 2-21 lowercase letters, digits, or hyphens."
  }
}

variable "environment" {
  type    = string
  default = "production"
}

variable "deployment_mode" {
  description = "Soulacy variant: personal, team, or scale."
  type        = string
  default     = "scale"
  validation {
    condition     = contains(["personal", "team", "scale"], var.deployment_mode)
    error_message = "deployment_mode must be personal, team, or scale."
  }
}

variable "domain_name" {
  description = "Public DNS name, for example soulacy.example.com."
  type        = string
}

variable "route53_zone_id" {
  description = "Public Route 53 hosted-zone ID that owns domain_name."
  type        = string
}

variable "oidc_issuer" {
  description = "OIDC discovery issuer URL. Keep empty only when bootstrapping with the platform API key first."
  type        = string
  default     = ""
}

variable "oidc_client_id" {
  description = "OIDC client/audience for the Soulacy web application."
  type        = string
  default     = ""
}

variable "gateway_image" {
  description = "Private ECR Soulacy image pinned by sha256 digest."
  type        = string
  validation {
    condition     = strcontains(var.gateway_image, "@sha256:")
    error_message = "gateway_image must be digest pinned."
  }
}

variable "execution_image" {
  description = "Private ECR execution image pinned by sha256 digest and signed by the deployment script."
  type        = string
  validation {
    condition     = strcontains(var.execution_image, "@sha256:")
    error_message = "execution_image must be digest pinned."
  }
}

variable "nats_image" {
  description = "Official NATS image pinned by sha256 digest."
  type        = string
  validation {
    condition     = strcontains(var.nats_image, "@sha256:")
    error_message = "nats_image must be digest pinned."
  }
}

variable "bootstrap_secret_name" {
  description = "Existing Secrets Manager secret populated by deploy.sh."
  type        = string
}

variable "nats_tls_secret_name" {
  description = "Existing Secrets Manager secret containing the private NATS CA and identities."
  type        = string
}

variable "vpc_cidr" {
  type    = string
  default = "10.42.0.0/16"
}

variable "gateway_instance_type" {
  type    = string
  default = "m7i.large"
}

variable "worker_instance_type" {
  type    = string
  default = "m7i.xlarge"
}

variable "nats_instance_type" {
  type    = string
  default = "t3.small"
}

variable "db_instance_class" {
  type    = string
  default = "db.t4g.medium"
}

variable "redis_node_type" {
  type    = string
  default = "cache.t4g.small"
}

variable "worker_concurrency" {
  type    = number
  default = 4
}

variable "cosign_version" {
  type    = string
  default = "v3.1.2"
}

variable "enable_deletion_protection" {
  type    = bool
  default = true
}

variable "enable_waf" {
  description = "Attach AWS managed WAF rules and an IP rate limit to the public ALB."
  type        = bool
  default     = true
}

variable "tags" {
  type    = map(string)
  default = {}
}
