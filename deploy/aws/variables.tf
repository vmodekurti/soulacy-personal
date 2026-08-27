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

variable "infrastructure_profile" {
  description = "AWS topology sizing: standard for production defaults, budget for a temporary small Team pilot."
  type        = string
  default     = "standard"
  validation {
    condition     = contains(["standard", "budget"], var.infrastructure_profile)
    error_message = "infrastructure_profile must be standard or budget."
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

variable "monthly_budget_usd" {
  description = "Monthly AWS budget created for the budget profile."
  type        = number
  default     = 180
  validation {
    condition     = var.monthly_budget_usd > 0 && var.monthly_budget_usd <= 10000
    error_message = "monthly_budget_usd must be greater than zero and no more than 10000."
  }
}

variable "budget_alert_email" {
  description = "Optional email for 50%, 80%, and 100% forecast/actual AWS budget alerts."
  type        = string
  default     = ""
  validation {
    condition     = var.budget_alert_email == "" || can(regex("^[^@[:space:]]+@[^@[:space:]]+\\.[^@[:space:]]+$", var.budget_alert_email))
    error_message = "budget_alert_email must be empty or a valid email address."
  }
}

variable "enable_off_hours_schedule" {
  description = "Automatically stop Team Lite compute/database after hours and restart them in the morning."
  type        = bool
  default     = true
}

variable "off_hours_timezone" {
  description = "IANA timezone used by the Team Lite start/stop schedule."
  type        = string
  default     = "America/Chicago"
  validation {
    condition     = can(regex("^[A-Za-z_]+/[A-Za-z0-9_+.-]+$", var.off_hours_timezone))
    error_message = "off_hours_timezone must be an IANA timezone such as America/Chicago."
  }
}

variable "off_hours_start_hour" {
  description = "Local hour (0-23) when Team Lite begins starting; EC2 follows after PostgreSQL."
  type        = number
  default     = 8
  validation {
    condition     = floor(var.off_hours_start_hour) == var.off_hours_start_hour && var.off_hours_start_hour >= 0 && var.off_hours_start_hour <= 23
    error_message = "off_hours_start_hour must be an integer from 0 through 23."
  }
}

variable "off_hours_stop_hour" {
  description = "Local hour (0-23) when Team Lite begins stopping; PostgreSQL follows EC2."
  type        = number
  default     = 22
  validation {
    condition     = floor(var.off_hours_stop_hour) == var.off_hours_stop_hour && var.off_hours_stop_hour >= 0 && var.off_hours_stop_hour <= 23
    error_message = "off_hours_stop_hour must be an integer from 0 through 23."
  }
}

variable "tags" {
  type    = map(string)
  default = {}
}
