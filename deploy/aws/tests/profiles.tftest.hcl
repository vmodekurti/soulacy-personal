mock_provider "aws" {
  mock_data "aws_availability_zones" {
    defaults = { names = ["us-east-1a", "us-east-1b", "us-east-1c"] }
  }
  mock_data "aws_iam_policy_document" {
    defaults = { json = "{\"Version\":\"2012-10-17\",\"Statement\":[]}" }
  }
  mock_data "aws_partition" {
    defaults = { partition = "aws" }
  }
  mock_resource "aws_acm_certificate" {
    defaults = {
      domain_validation_options = [{
        domain_name           = "soulacy.example.com"
        resource_record_name  = "_test.soulacy.example.com"
        resource_record_type  = "CNAME"
        resource_record_value = "_validation.example.com"
      }]
    }
  }
}

variables {
  aws_region                 = "us-east-1"
  domain_name                = "soulacy.example.com"
  route53_zone_id            = "ZTEST"
  gateway_image              = "example.com/soulacy@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
  execution_image            = "example.com/execution@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
  nats_image                 = "nats@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
  bootstrap_secret_name      = "soulacy/test/bootstrap"
  nats_tls_secret_name       = "soulacy/test/nats"
  enable_deletion_protection = false
}

run "personal_omits_multi_user_services" {
  command = plan
  variables { deployment_mode = "personal" }

  assert {
    condition     = length(aws_instance.worker) == 0 && length(aws_instance.nats) == 0
    error_message = "Personal must not provision worker or NATS instances."
  }
  assert {
    condition     = length(aws_db_instance.postgres) == 0 && length(aws_elasticache_replication_group.redis) == 0 && length(aws_s3_bucket.artifacts) == 0
    error_message = "Personal must use embedded stores and omit RDS, Redis, and artifact S3."
  }
  assert {
    condition     = length(aws_kms_key.credentials) == 0
    error_message = "Personal must not create the multi-tenant credential KMS key."
  }
}

run "team_adds_multi_user_plane" {
  command = plan
  variables { deployment_mode = "team" }

  assert {
    condition     = length(aws_instance.worker) == 1 && length(aws_instance.nats) == 1 && length(aws_db_instance.postgres) == 1
    error_message = "Team must provision the worker, NATS, and PostgreSQL plane."
  }
  assert {
    condition     = length(aws_kms_key.credentials) == 1
    error_message = "Team must provision external credential KMS."
  }
  assert {
    condition     = length(aws_elasticache_replication_group.redis) == 0 && length(aws_s3_bucket.artifacts) == 0
    error_message = "Team must omit Scale-only Redis and artifact S3."
  }
  assert {
    condition     = length(aws_scheduler_schedule.start_compute) == 0 && length(aws_scheduler_schedule.stop_compute) == 0
    error_message = "Standard Team must not receive the temporary pilot power schedule."
  }
}

run "team_lite_preserves_team_boundaries_on_budget_resources" {
  command = plan
  variables {
    deployment_mode        = "team"
    infrastructure_profile = "budget"
    budget_alert_email     = "operator@example.com"
  }

  assert {
    condition     = length(aws_instance.worker) == 1 && length(aws_instance.nats) == 1 && length(aws_db_instance.postgres) == 1
    error_message = "Team Lite must preserve the separate worker, NATS, and PostgreSQL boundaries."
  }
  assert {
    condition     = aws_instance.gateway.instance_type == "t3.small" && aws_instance.worker[0].instance_type == "t3.small" && aws_instance.nats[0].instance_type == "t3.micro"
    error_message = "Team Lite must use its cost-bounded burstable instance sizes."
  }
  assert {
    condition     = aws_db_instance.postgres[0].instance_class == "db.t4g.micro" && !aws_db_instance.postgres[0].multi_az && aws_db_instance.postgres[0].allocated_storage == 20
    error_message = "Team Lite must use the temporary Single-AZ 20 GB database profile."
  }
  assert {
    condition     = length(aws_nat_gateway.this) == 0 && length(aws_eip.nat) == 0
    error_message = "Team Lite must avoid the fixed NAT Gateway and EIP charges."
  }
  assert {
    condition     = aws_instance.gateway.associate_public_ip_address && aws_instance.worker[0].associate_public_ip_address && aws_instance.nats[0].associate_public_ip_address
    error_message = "Team Lite compute needs outbound internet through public IPs when NAT is omitted."
  }
  assert {
    condition     = length(aws_budgets_budget.pilot) == 1 && aws_budgets_budget.pilot[0].limit_amount == "180"
    error_message = "Team Lite must install the monthly account budget guardrail."
  }
  assert {
    condition     = length(aws_scheduler_schedule.start_compute) == 1 && length(aws_scheduler_schedule.stop_compute) == 1 && length(aws_scheduler_schedule.start_database) == 1 && length(aws_scheduler_schedule.stop_database) == 1
    error_message = "Team Lite must install the four ordered compute/database power schedules."
  }
  assert {
    condition     = aws_scheduler_schedule.start_compute[0].schedule_expression == "cron(15 8 * * ? *)" && aws_scheduler_schedule.stop_compute[0].schedule_expression == "cron(0 22 * * ? *)" && aws_scheduler_schedule.start_compute[0].schedule_expression_timezone == "America/Chicago"
    error_message = "Team Lite must default to the documented 08:00-22:00 America/Chicago schedule."
  }
}

run "scale_adds_shared_services" {
  command = plan
  variables { deployment_mode = "scale" }

  assert {
    condition     = length(aws_instance.worker) == 1 && length(aws_instance.nats) == 1 && length(aws_db_instance.postgres) == 1
    error_message = "Scale must include the Team execution and storage plane."
  }
  assert {
    condition     = length(aws_elasticache_replication_group.redis) == 1 && length(aws_s3_bucket.artifacts) == 1
    error_message = "Scale must provision shared Redis and S3 artifacts."
  }
}
