#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
AWS_DIR="$ROOT_DIR/deploy/aws"
ENV_FILE="$AWS_DIR/.deployment.env"
ACTION="${1:-}"
[[ -f "$ENV_FILE" ]] && set -a && source "$ENV_FILE" && set +a

usage() {
  cat <<'EOF'
Usage:
  deploy/aws/public-demo.sh enable --workspace-id ID --provider NAME --model NAME [options]
  deploy/aws/public-demo.sh disable
  deploy/aws/public-demo.sh status

Options: --tools CSV --membership-ttl 24h --draft-ttl 24h
         --max-active-members 100 --per-user-rpm 20 --per-user-tokens-day 50000
EOF
}
[[ "$ACTION" == enable || "$ACTION" == disable || "$ACTION" == status ]] || { usage; exit 2; }
shift

WORKSPACE_ID=""; PROVIDER=""; MODEL=""; TOOLS="web_search,fetch_url,generate_chart"
MEMBERSHIP_TTL=24h; DRAFT_TTL=24h; MAX_MEMBERS=100; USER_RPM=20; USER_TOKENS=50000
while (($#)); do
  case "$1" in
    --workspace-id) WORKSPACE_ID="$2"; shift 2;; --provider) PROVIDER="$2"; shift 2;; --model) MODEL="$2"; shift 2;;
    --tools) TOOLS="$2"; shift 2;; --membership-ttl) MEMBERSHIP_TTL="$2"; shift 2;; --draft-ttl) DRAFT_TTL="$2"; shift 2;;
    --max-active-members) MAX_MEMBERS="$2"; shift 2;; --per-user-rpm) USER_RPM="$2"; shift 2;;
    --per-user-tokens-day) USER_TOKENS="$2"; shift 2;; *) printf 'Unknown argument: %s\n' "$1" >&2; usage; exit 2;;
  esac
done
if [[ "$ACTION" == enable ]]; then
  [[ -n "$WORKSPACE_ID" && -n "$PROVIDER" && -n "$MODEL" ]] || { printf 'enable requires workspace, provider, and model\n' >&2; exit 2; }
  [[ "$WORKSPACE_ID" =~ ^ws_[A-Za-z0-9]+$ ]] || { printf 'invalid immutable workspace ID: %s\n' "$WORKSPACE_ID" >&2; exit 2; }
fi

: "${AWS_PROFILE:?AWS_PROFILE is missing from deploy/aws/.deployment.env}"
: "${AWS_REGION:?AWS_REGION is missing from deploy/aws/.deployment.env}"
command -v aws >/dev/null || { printf 'AWS CLI is required\n' >&2; exit 1; }
command -v terraform >/dev/null || { printf 'Terraform is required\n' >&2; exit 1; }

CREDS="$(aws configure export-credentials --profile "$AWS_PROFILE" --format env 2>/dev/null)" || {
  printf 'AWS credentials unavailable. Run: aws login --profile %s\n' "$AWS_PROFILE" >&2; exit 1;
}
eval "$CREDS"
export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION"

if [[ "$ACTION" != status ]]; then "$AWS_DIR/power.sh" start >/dev/null; fi
terraform -chdir="$AWS_DIR" init -reconfigure -input=false \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" \
  -backend-config="use_lockfile=true" >/dev/null
INSTANCE_ID="$(terraform -chdir="$AWS_DIR" output -raw gateway_instance_id)"
GATEWAY_URL="$(terraform -chdir="$AWS_DIR" output -raw url)"
[[ -n "$INSTANCE_ID" ]] || { printf 'gateway instance output is empty\n' >&2; exit 1; }
SSM_ONLINE=false
for _ in {1..90}; do
  PING="$(aws ssm describe-instance-information --region "$AWS_REGION" \
    --filters "Key=InstanceIds,Values=$INSTANCE_ID" \
    --query 'InstanceInformationList[0].PingStatus' --output text 2>/dev/null || true)"
  [[ "$PING" == Online ]] && { SSM_ONLINE=true; break; }
  sleep 5
done
[[ "$SSM_ONLINE" == true ]] || { printf 'gateway did not become available in Systems Manager\n' >&2; exit 1; }

VARIANT="${INSTALL_VARIANT:-${SOULACY_VARIANT:-team-lite}}"; LOCAL_REDIS=false; REDIS_URL=""
case "$VARIANT" in
  team-lite) LOCAL_REDIS=true; [[ "$ACTION" == enable ]] && REDIS_URL='redis://127.0.0.1:6379';;
  scale)
    if [[ "$ACTION" == enable ]]; then
      REDIS_ENDPOINT="$(terraform -chdir="$AWS_DIR" output -raw redis_endpoint)"
      REDIS_URL="rediss://$REDIS_ENDPOINT:6379"
    fi
    ;;
  *) printf 'Public demo is supported by Team Lite and Scale, not %s\n' "$VARIANT" >&2; exit 1;;
esac

SETTINGS_JSON_BASE64="$(python3 - "$WORKSPACE_ID" "$PROVIDER" "$MODEL" "$TOOLS" "$MEMBERSHIP_TTL" "$DRAFT_TTL" "$MAX_MEMBERS" "$USER_RPM" "$USER_TOKENS" "$REDIS_URL" <<'PY' | base64 | tr -d '\n'
import json, sys
w,p,m,tools,mt,dt,mm,rpm,tokens,redis=sys.argv[1:]
print(json.dumps({"enabled":True,"workspace_id":w,"membership_ttl":mt,"draft_ttl":dt,
 "max_active_members":int(mm),"allowed_providers":[p],"allowed_models":[m],
 "allowed_tools":[x.strip() for x in tools.split(',') if x.strip()],"backend":"redis",
 "redis_url":redis,"per_user_rpm":int(rpm),"per_user_tokens_day":int(tokens)}))
PY
)"
REMOTE_SCRIPT_BASE64="$(base64 < "$AWS_DIR/remote-public-demo.sh" | tr -d '\n')"
COMMAND="export SETTINGS_JSON_BASE64='$SETTINGS_JSON_BASE64' LOCAL_REDIS='$LOCAL_REDIS'; printf '%s' '$REMOTE_SCRIPT_BASE64' | base64 --decode > /tmp/soulacy-public-demo.sh; chmod 700 /tmp/soulacy-public-demo.sh; /tmp/soulacy-public-demo.sh '$ACTION'"
PARAMETERS="$(python3 - "$COMMAND" <<'PY'
import json, sys
print(json.dumps({"commands": [sys.argv[1]]}))
PY
)"
COMMAND_ID="$(aws ssm send-command --region "$AWS_REGION" --instance-ids "$INSTANCE_ID" --document-name AWS-RunShellScript --parameters "$PARAMETERS" --query 'Command.CommandId' --output text)"
set +e
WAIT_STATUS=1
for _ in {1..120}; do
  COMMAND_STATUS="$(aws ssm get-command-invocation --region "$AWS_REGION" --command-id "$COMMAND_ID" --instance-id "$INSTANCE_ID" --query Status --output text 2>/dev/null || true)"
  case "$COMMAND_STATUS" in
    Success) WAIT_STATUS=0; break ;;
    Failed|Cancelled|Cancelling|TimedOut) break ;;
  esac
  sleep 5
done
set -e
RESULT="$(aws ssm get-command-invocation --region "$AWS_REGION" --command-id "$COMMAND_ID" --instance-id "$INSTANCE_ID")"
printf '%s\n' "$RESULT" | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d.get("StandardOutputContent",""),end=""); print(d.get("StandardErrorContent",""),end="",file=sys.stderr)'
[[ $WAIT_STATUS -eq 0 ]] || exit 1
if [[ "$ACTION" != status ]]; then
  for _ in {1..30}; do curl -fsS "$GATEWAY_URL/ready" >/dev/null 2>&1 && { printf 'Gateway ready: %s\n' "$GATEWAY_URL"; exit 0; }; sleep 2; done
  printf 'Gateway did not become externally ready: %s\n' "$GATEWAY_URL" >&2; exit 1
fi
