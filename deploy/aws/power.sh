#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
ENV_FILE="$SCRIPT_DIR/.deployment.env"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
usage() {
  cat <<'USAGE'
Usage: power.sh start|stop|status

Starts or stops Team/Team Lite EC2 and PostgreSQL resources without deleting
workspace data or the public edge. The AWS-native Team Lite schedule invokes
the equivalent APIs automatically; this command is for manual overrides.
USAGE
}

[[ $# -eq 1 ]] || { usage; exit 1; }
ACTION="$1"
[[ "$ACTION" =~ ^(start|stop|status)$ ]] || { usage; exit 1; }
[[ -f "$ENV_FILE" ]] || die "Run deploy.sh first; $ENV_FILE is missing"
for command in aws terraform jq; do need "$command"; done

# shellcheck disable=SC1091
source "$ENV_FILE"
[[ -n "${AWS_PROFILE:-}" ]] && export AWS_PROFILE
[[ "${DEPLOYMENT_MODE:-}" == "team" || "${DEPLOYMENT_MODE:-}" == "scale" ]] || die "power control requires Team or Scale mode"

terraform -chdir="$SCRIPT_DIR" init -reconfigure -input=false \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" -backend-config="use_lockfile=true" >/dev/null
outputs=$(terraform -chdir="$SCRIPT_DIR" output -json)
instance_ids=()
while IFS= read -r id; do [[ -n "$id" ]] && instance_ids+=("$id"); done < <(
  jq -r '[.gateway_instance_id.value, .worker_instance_id.value, .nats_instance_id.value] | .[] | select(. != null and . != "")' <<<"$outputs"
)
db_identifier=$(jq -r '.rds_instance_identifier.value // empty' <<<"$outputs")
[[ ${#instance_ids[@]} -gt 0 ]] || die "no EC2 instances found in Terraform outputs"

database_status() {
  [[ -n "$db_identifier" ]] || { printf 'not-applicable'; return; }
  aws rds describe-db-instances --region "$AWS_REGION" --db-instance-identifier "$db_identifier" \
    --query 'DBInstances[0].DBInstanceStatus' --output text
}

show_status() {
  printf 'Variant: %s\n' "${INSTALL_VARIANT:-$DEPLOYMENT_MODE}"
  aws ec2 describe-instances --region "$AWS_REGION" --instance-ids "${instance_ids[@]}" \
    --query 'Reservations[].Instances[].{Name:Tags[?Key==`Name`]|[0].Value,Instance:InstanceId,State:State.Name,Type:InstanceType}' --output table
  printf 'PostgreSQL %s: %s\n' "${db_identifier:-n/a}" "$(database_status)"
}

case "$ACTION" in
  start)
    db_status=$(database_status)
    case "$db_status" in
      stopped)
        printf 'Starting PostgreSQL %s...\n' "$db_identifier"
        aws rds start-db-instance --region "$AWS_REGION" --db-instance-identifier "$db_identifier" >/dev/null
        ;;
      available) printf 'PostgreSQL is already available.\n' ;;
      starting) printf 'PostgreSQL is already starting.\n' ;;
      *) die "PostgreSQL cannot be started while its status is $db_status" ;;
    esac
    printf 'Waiting for PostgreSQL to become available...\n'
    aws rds wait db-instance-available --region "$AWS_REGION" --db-instance-identifier "$db_identifier"
    printf 'Starting EC2 gateway, worker, and NATS...\n'
    aws ec2 start-instances --region "$AWS_REGION" --instance-ids "${instance_ids[@]}" >/dev/null
    aws ec2 wait instance-running --region "$AWS_REGION" --instance-ids "${instance_ids[@]}"
    printf 'Compute is running. Allow several minutes for Soulacy /ready to become healthy.\n'
    ;;
  stop)
    printf 'Stopping EC2 gateway, worker, and NATS...\n'
    aws ec2 stop-instances --region "$AWS_REGION" --instance-ids "${instance_ids[@]}" >/dev/null
    aws ec2 wait instance-stopped --region "$AWS_REGION" --instance-ids "${instance_ids[@]}"
    db_status=$(database_status)
    case "$db_status" in
      available)
        printf 'Stopping PostgreSQL %s...\n' "$db_identifier"
        aws rds stop-db-instance --region "$AWS_REGION" --db-instance-identifier "$db_identifier" >/dev/null
        ;;
      stopped) printf 'PostgreSQL is already stopped.\n' ;;
      stopping) printf 'PostgreSQL is already stopping.\n' ;;
      *) die "PostgreSQL cannot be stopped while its status is $db_status" ;;
    esac
    printf 'Off-hours shutdown requested. The configured edge, EFS, KMS, EBS, DNS, ECR, and secrets remain provisioned.\n'
    ;;
  status) show_status ;;
esac
