#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -f "$SCRIPT_DIR/.deployment.env" ]] || { echo "Run deploy.sh first." >&2; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.deployment.env"
DEPLOYMENT_MODE="${DEPLOYMENT_MODE:-scale}"
if [[ "${SOULACY_AWS_DESTROY_CONFIRM:-}" != "$DEPLOYMENT_NAME" ]]; then
  echo "Refusing destructive teardown. Set SOULACY_AWS_DESTROY_CONFIRM=$DEPLOYMENT_NAME and retry." >&2
  exit 1
fi
terraform -chdir="$SCRIPT_DIR" init -reconfigure \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" -backend-config="use_lockfile=true"
terraform -chdir="$SCRIPT_DIR" destroy -auto-approve -var-file="$VAR_FILE" \
  -var="aws_region=$AWS_REGION" -var="name=$DEPLOYMENT_NAME" -var="environment=$ENVIRONMENT" -var="deployment_mode=$DEPLOYMENT_MODE" \
  -var="gateway_image=$GATEWAY_IMAGE" -var="execution_image=$EXECUTION_IMAGE" -var="nats_image=$NATS_IMAGE" \
  -var="bootstrap_secret_name=$BOOTSTRAP_SECRET" -var="nats_tls_secret_name=$NATS_SECRET"
echo "Terraform resources were destroyed. State, ECR repositories, and Secrets Manager recovery material were intentionally retained."
