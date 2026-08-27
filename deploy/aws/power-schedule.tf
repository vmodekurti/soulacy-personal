locals {
  power_schedule_enabled = local.is_budget && var.enable_off_hours_schedule
  power_instance_ids = local.is_multi_user ? [
    aws_instance.gateway.id,
    aws_instance.worker[0].id,
    aws_instance.nats[0].id,
  ] : [aws_instance.gateway.id]
}

data "aws_iam_policy_document" "scheduler_assume" {
  count = local.power_schedule_enabled ? 1 : 0
  statement {
    actions = ["sts:AssumeRole"]
    principals {
      type        = "Service"
      identifiers = ["scheduler.amazonaws.com"]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceAccount"
      values   = [data.aws_caller_identity.current.account_id]
    }
    condition {
      test     = "StringEquals"
      variable = "aws:SourceArn"
      values   = ["arn:${data.aws_partition.current.partition}:scheduler:${var.aws_region}:${data.aws_caller_identity.current.account_id}:schedule-group/${local.prefix}-power"]
    }
  }
}

resource "aws_iam_role" "power_scheduler" {
  count              = local.power_schedule_enabled ? 1 : 0
  name_prefix        = "${local.prefix}-power-"
  assume_role_policy = data.aws_iam_policy_document.scheduler_assume[0].json
}

data "aws_iam_policy_document" "power_scheduler" {
  count = local.power_schedule_enabled ? 1 : 0
  statement {
    sid       = "ManagePilotCompute"
    actions   = ["ec2:StartInstances", "ec2:StopInstances"]
    resources = [for id in local.power_instance_ids : "arn:${data.aws_partition.current.partition}:ec2:${var.aws_region}:${data.aws_caller_identity.current.account_id}:instance/${id}"]
  }
  statement {
    sid       = "ManagePilotDatabase"
    actions   = ["rds:StartDBInstance", "rds:StopDBInstance"]
    resources = [aws_db_instance.postgres[0].arn]
  }
}

resource "aws_iam_role_policy" "power_scheduler" {
  count  = local.power_schedule_enabled ? 1 : 0
  role   = aws_iam_role.power_scheduler[0].id
  policy = data.aws_iam_policy_document.power_scheduler[0].json
}

resource "aws_scheduler_schedule_group" "power" {
  count = local.power_schedule_enabled ? 1 : 0
  name  = "${local.prefix}-power"
}

resource "aws_scheduler_schedule" "stop_compute" {
  count      = local.power_schedule_enabled ? 1 : 0
  name       = "${local.prefix}-stop-compute"
  group_name = aws_scheduler_schedule_group.power[0].name
  state      = "ENABLED"

  flexible_time_window { mode = "OFF" }
  schedule_expression          = "cron(0 ${var.off_hours_stop_hour} * * ? *)"
  schedule_expression_timezone = var.off_hours_timezone

  target {
    arn      = "arn:${data.aws_partition.current.partition}:scheduler:::aws-sdk:ec2:stopInstances"
    role_arn = aws_iam_role.power_scheduler[0].arn
    input    = jsonencode({ InstanceIds = local.power_instance_ids })
    retry_policy { maximum_retry_attempts = 2 }
  }
}

resource "aws_scheduler_schedule" "stop_database" {
  count      = local.power_schedule_enabled ? 1 : 0
  name       = "${local.prefix}-stop-database"
  group_name = aws_scheduler_schedule_group.power[0].name
  state      = "ENABLED"

  flexible_time_window { mode = "OFF" }
  schedule_expression          = "cron(10 ${var.off_hours_stop_hour} * * ? *)"
  schedule_expression_timezone = var.off_hours_timezone

  target {
    arn      = "arn:${data.aws_partition.current.partition}:scheduler:::aws-sdk:rds:stopDBInstance"
    role_arn = aws_iam_role.power_scheduler[0].arn
    input    = jsonencode({ DbInstanceIdentifier = aws_db_instance.postgres[0].identifier })
    retry_policy { maximum_retry_attempts = 2 }
  }
}

resource "aws_scheduler_schedule" "start_database" {
  count      = local.power_schedule_enabled ? 1 : 0
  name       = "${local.prefix}-start-database"
  group_name = aws_scheduler_schedule_group.power[0].name
  state      = "ENABLED"

  flexible_time_window { mode = "OFF" }
  schedule_expression          = "cron(0 ${var.off_hours_start_hour} * * ? *)"
  schedule_expression_timezone = var.off_hours_timezone

  target {
    arn      = "arn:${data.aws_partition.current.partition}:scheduler:::aws-sdk:rds:startDBInstance"
    role_arn = aws_iam_role.power_scheduler[0].arn
    input    = jsonencode({ DbInstanceIdentifier = aws_db_instance.postgres[0].identifier })
    retry_policy { maximum_retry_attempts = 2 }
  }
}

resource "aws_scheduler_schedule" "start_compute" {
  count      = local.power_schedule_enabled ? 1 : 0
  name       = "${local.prefix}-start-compute"
  group_name = aws_scheduler_schedule_group.power[0].name
  state      = "ENABLED"

  flexible_time_window { mode = "OFF" }
  schedule_expression          = "cron(15 ${var.off_hours_start_hour} * * ? *)"
  schedule_expression_timezone = var.off_hours_timezone

  target {
    arn      = "arn:${data.aws_partition.current.partition}:scheduler:::aws-sdk:ec2:startInstances"
    role_arn = aws_iam_role.power_scheduler[0].arn
    input    = jsonencode({ InstanceIds = local.power_instance_ids })
    retry_policy { maximum_retry_attempts = 2 }
  }
}
