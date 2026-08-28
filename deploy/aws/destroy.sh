#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -f "$SCRIPT_DIR/.deployment.env" ]] || { echo "Run deploy.sh first." >&2; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.deployment.env"
[[ -n "${AWS_PROFILE:-}" ]] && export AWS_PROFILE
DEPLOYMENT_MODE="${DEPLOYMENT_MODE:-scale}"
INFRASTRUCTURE_PROFILE="${INFRASTRUCTURE_PROFILE:-standard}"
INGRESS_MODE="${INGRESS_MODE:-alb}"
BUDGET_EMAIL="${BUDGET_EMAIL:-}"
MONTHLY_BUDGET_USD="${MONTHLY_BUDGET_USD:-180}"
OFF_HOURS_TIMEZONE="${OFF_HOURS_TIMEZONE:-America/Chicago}"
OFF_HOURS_START="${OFF_HOURS_START:-8}"
OFF_HOURS_STOP="${OFF_HOURS_STOP:-22}"
if [[ "${SOULACY_AWS_DESTROY_CONFIRM:-}" != "$DEPLOYMENT_NAME" ]]; then
  echo "Refusing destructive teardown. Set SOULACY_AWS_DESTROY_CONFIRM=$DEPLOYMENT_NAME and retry." >&2
  exit 1
fi
terraform -chdir="$SCRIPT_DIR" init -reconfigure \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" -backend-config="use_lockfile=true"
terraform -chdir="$SCRIPT_DIR" destroy -auto-approve -var-file="$VAR_FILE" \
  -var="aws_region=$AWS_REGION" -var="name=$DEPLOYMENT_NAME" -var="environment=$ENVIRONMENT" -var="deployment_mode=$DEPLOYMENT_MODE" \
  -var="infrastructure_profile=$INFRASTRUCTURE_PROFILE" -var="budget_alert_email=$BUDGET_EMAIL" -var="monthly_budget_usd=$MONTHLY_BUDGET_USD" \
  -var="off_hours_timezone=$OFF_HOURS_TIMEZONE" -var="off_hours_start_hour=$OFF_HOURS_START" -var="off_hours_stop_hour=$OFF_HOURS_STOP" \
  -var="ingress_mode=$INGRESS_MODE" -var="gateway_image=$GATEWAY_IMAGE" -var="execution_image=$EXECUTION_IMAGE" -var="nats_image=$NATS_IMAGE" -var="cloudflared_image=${CLOUDFLARED_IMAGE:-cloudflare/cloudflared@sha256:0000000000000000000000000000000000000000000000000000000000000000}" \
  -var="bootstrap_secret_name=$BOOTSTRAP_SECRET" -var="nats_tls_secret_name=$NATS_SECRET"
echo "Terraform resources were destroyed. State, ECR repositories, and Secrets Manager recovery material were intentionally retained."
