output "url" {
  value = "https://${var.domain_name}"
}

output "infrastructure_profile" {
  value = var.infrastructure_profile
}

output "ingress_mode" {
  value = local.use_cloudflare_tunnel ? "cloudflare_tunnel" : "alb"
}

output "monthly_budget_usd" {
  value = local.is_budget ? var.monthly_budget_usd : null
}

output "gateway_instance_id" {
  value = aws_instance.gateway.id
}

output "worker_instance_id" {
  value = local.is_multi_user ? aws_instance.worker[0].id : null
}

output "nats_instance_id" {
  value = local.is_multi_user ? aws_instance.nats[0].id : null
}

output "artifact_bucket" {
  value = local.is_scale ? aws_s3_bucket.artifacts[0].id : null
}

output "credential_kms_key" {
  value = local.is_multi_user ? aws_kms_key.credentials[0].arn : null
}

output "rds_endpoint" {
  value     = local.is_multi_user ? aws_db_instance.postgres[0].address : null
  sensitive = true
}

output "rds_instance_identifier" {
  value = local.is_multi_user ? aws_db_instance.postgres[0].identifier : null
}

output "off_hours_schedule" {
  value = local.power_schedule_enabled ? {
    timezone     = var.off_hours_timezone
    database_on  = format("%02d:00", var.off_hours_start_hour)
    compute_on   = format("%02d:15", var.off_hours_start_hour)
    compute_off  = format("%02d:00", var.off_hours_stop_hour)
    database_off = format("%02d:10", var.off_hours_stop_hour)
  } : null
}

output "redis_endpoint" {
  value     = local.is_scale ? aws_elasticache_replication_group.redis[0].primary_endpoint_address : null
  sensitive = true
}

output "ssm_gateway_command" {
  value = "aws ssm start-session --region ${var.aws_region} --target ${aws_instance.gateway.id}"
}
