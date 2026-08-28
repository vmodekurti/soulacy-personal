#!/usr/bin/env bash
set -Eeuo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
[[ -f "$SCRIPT_DIR/.deployment.env" ]] || { echo "Run deploy.sh first." >&2; exit 1; }
# shellcheck disable=SC1091
source "$SCRIPT_DIR/.deployment.env"
[[ -n "${AWS_PROFILE:-}" ]] && export AWS_PROFILE
DEPLOYMENT_MODE="${DEPLOYMENT_MODE:-scale}"
INSTALL_VARIANT="${INSTALL_VARIANT:-$DEPLOYMENT_MODE}"
INFRASTRUCTURE_PROFILE="${INFRASTRUCTURE_PROFILE:-standard}"
terraform -chdir="$SCRIPT_DIR" init -reconfigure -input=false \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" -backend-config="use_lockfile=true" >/dev/null
url=$(terraform -chdir="$SCRIPT_DIR" output -raw url)
outputs=$(terraform -chdir="$SCRIPT_DIR" output -json)
ingress=$(jq -r '.ingress_mode.value // "alb"' <<<"$outputs")
gateway=$(jq -r '.gateway_instance_id.value' <<<"$outputs")
worker=$(jq -r '.worker_instance_id.value // empty' <<<"$outputs")
nats=$(jq -r '.nats_instance_id.value // empty' <<<"$outputs")
printf 'Variant: %s (application mode: %s, infrastructure: %s)\nIngress: %s\nURL: %s\n' "$INSTALL_VARIANT" "$DEPLOYMENT_MODE" "$INFRASTRUCTURE_PROFILE" "$ingress" "$url"
curl --fail --silent --show-error "$url/ready" | jq . || true
instance_ids=("$gateway")
[[ -n "$worker" ]] && instance_ids+=("$worker")
[[ -n "$nats" ]] && instance_ids+=("$nats")
aws ec2 describe-instance-status --region "$AWS_REGION" --include-all-instances --instance-ids "${instance_ids[@]}" \
  --query 'InstanceStatuses[].{Instance:InstanceId,State:InstanceState.Name,System:SystemStatus.Status,InstanceCheck:InstanceStatus.Status}' --output table
printf '\nGateway logs: aws ssm start-session --region %s --target %s\n' "$AWS_REGION" "$gateway"
printf 'Then run: sudo journalctl -u soulacy -f\n'
if [[ "$ingress" == "cloudflare_tunnel" ]]; then
  printf 'Tunnel logs: sudo journalctl -u soulacy-cloudflared -f\n'
fi
