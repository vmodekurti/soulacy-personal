output "url" {
  value = "https://${var.domain_name}"
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

output "redis_endpoint" {
  value     = local.is_scale ? aws_elasticache_replication_group.redis[0].primary_endpoint_address : null
  sensitive = true
}

output "ssm_gateway_command" {
  value = "aws ssm start-session --region ${var.aws_region} --target ${aws_instance.gateway.id}"
}
