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
