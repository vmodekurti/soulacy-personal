#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../.." && pwd)
ENV_FILE="$SCRIPT_DIR/.deployment.env"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
[[ $# -eq 0 ]] || die "release.sh takes no arguments; it deploys the current committed checkout"
[[ -f "$ENV_FILE" ]] || die "Run the initial AWS deployment first; $ENV_FILE is missing"
for command in aws docker jq git cosign curl; do need "$command"; done
docker buildx version >/dev/null 2>&1 || die "Docker Buildx is required"

# shellcheck disable=SC1091
source "$ENV_FILE"
[[ -n "${AWS_PROFILE:-}" ]] && export AWS_PROFILE
[[ -n "${AWS_REGION:-}" ]] || die "AWS_REGION is missing from $ENV_FILE"
export AWS_REGION AWS_DEFAULT_REGION="$AWS_REGION"

if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" && "${SOULACY_AWS_ALLOW_DIRTY:-}" != "1" ]]; then
  die "commit the changes before deploying, or set SOULACY_AWS_ALLOW_DIRTY=1 for a disposable test"
fi
if [[ -n "${AWS_PROFILE:-}" && -z "${AWS_ACCESS_KEY_ID:-}" ]]; then
  credentials=$(aws configure export-credentials --profile "$AWS_PROFILE" --format process) || \
    die "could not export short-lived credentials from AWS profile $AWS_PROFILE"
  export AWS_ACCESS_KEY_ID=$(jq -er '.AccessKeyId' <<<"$credentials")
  export AWS_SECRET_ACCESS_KEY=$(jq -er '.SecretAccessKey' <<<"$credentials")
  export AWS_SESSION_TOKEN=$(jq -er '.SessionToken' <<<"$credentials")
fi

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
REGISTRY="$ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com"
GATEWAY_REPO="${DEPLOYMENT_NAME:-soulacy}-gateway"
EXECUTION_REPO="${DEPLOYMENT_NAME:-soulacy}-execution"
BUILD_TAG=$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD)
[[ -z "$(git -C "$REPO_ROOT" status --porcelain)" ]] || BUILD_TAG="$BUILD_TAG-dirty-$(date -u +%Y%m%d%H%M%S)"

if [[ "${SOULACY_RELEASE_SKIP_TESTS:-0}" != "1" ]]; then
  printf 'Running release regression tests...\n'
  (cd "$REPO_ROOT" && go test ./... -timeout 10m)
fi

printf 'Ensuring the AWS deployment is running...\n'
"$SCRIPT_DIR/power.sh" start

aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$REGISTRY" >/dev/null
if ! aws ecr describe-images --region "$AWS_REGION" --repository-name "$GATEWAY_REPO" --image-ids imageTag="$BUILD_TAG" >/dev/null 2>&1; then
  printf 'Building and publishing Soulacy %s...\n' "$BUILD_TAG"
  docker buildx build --platform linux/amd64 --provenance=false --push \
    --build-arg "VERSION=$BUILD_TAG" -t "$REGISTRY/$GATEWAY_REPO:$BUILD_TAG" "$REPO_ROOT"
fi
gateway_digest=$(aws ecr describe-images --region "$AWS_REGION" --repository-name "$GATEWAY_REPO" \
  --image-ids imageTag="$BUILD_TAG" --query 'imageDetails[0].imageDigest' --output text)
GATEWAY_IMAGE="$REGISTRY/$GATEWAY_REPO@$gateway_digest"

python_digest=$(docker buildx imagetools inspect python:3.12-slim | awk '/^Digest:/ {print $2; exit}')
[[ "$python_digest" == sha256:* ]] || die "could not resolve the Python execution base image"
if ! aws ecr describe-images --region "$AWS_REGION" --repository-name "$EXECUTION_REPO" --image-ids imageTag="$BUILD_TAG" >/dev/null 2>&1; then
  docker buildx build --platform linux/amd64 --provenance=false --push \
    --build-arg "PYTHON_IMAGE=python:3.12-slim@$python_digest" -f "$SCRIPT_DIR/Dockerfile.execution" \
    -t "$REGISTRY/$EXECUTION_REPO:$BUILD_TAG" "$SCRIPT_DIR"
fi
execution_digest=$(aws ecr describe-images --region "$AWS_REGION" --repository-name "$EXECUTION_REPO" \
  --image-ids imageTag="$BUILD_TAG" --query 'imageDetails[0].imageDigest' --output text)
EXECUTION_IMAGE="$REGISTRY/$EXECUTION_REPO@$execution_digest"

signing_alias="alias/${DEPLOYMENT_NAME:-soulacy}-${ENVIRONMENT:-production}-execution-signing"
signing_key=$(aws kms list-aliases --region "$AWS_REGION" \
  --query "Aliases[?AliasName=='$signing_alias'].TargetKeyId | [0]" --output text)
[[ -n "$signing_key" && "$signing_key" != "None" ]] || die "execution signing key $signing_alias was not found"
signing_arn=$(aws kms describe-key --region "$AWS_REGION" --key-id "$signing_key" --query KeyMetadata.Arn --output text)
cosign sign --yes --key "awskms:///$signing_arn" "$EXECUTION_IMAGE"

find_instance() {
  aws ec2 describe-instances --region "$AWS_REGION" \
    --filters "Name=tag:Name,Values=${DEPLOYMENT_NAME}-${ENVIRONMENT}-$1" \
      'Name=instance-state-name,Values=pending,running' \
    --query 'Reservations[0].Instances[0].InstanceId' --output text
}
gateway_id=$(find_instance gateway)
worker_id=$(find_instance worker)
[[ "$gateway_id" != "None" && "$worker_id" != "None" ]] || die "running gateway and worker instances were not found"

wait_for_ssm() {
  local instance_id=$1 ping
  for _ in {1..90}; do
    ping=$(aws ssm describe-instance-information --region "$AWS_REGION" \
      --filters "Key=InstanceIds,Values=$instance_id" --query 'InstanceInformationList[0].PingStatus' --output text)
    [[ "$ping" == "Online" ]] && return 0
    sleep 5
  done
  die "instance $instance_id did not become available in Systems Manager"
}

run_remote() {
  local instance_id=$1 action=$2 role=$3 script_b64 command parameters command_id state
  script_b64=$(base64 <"$SCRIPT_DIR/remote-release.sh" | tr -d '\n')
  command="printf '%s' '$script_b64' | base64 -d >/tmp/soulacy-remote-release.sh && chmod 0700 /tmp/soulacy-remote-release.sh && sudo /tmp/soulacy-remote-release.sh '$action' '$role' '$BUILD_TAG' '$GATEWAY_IMAGE' '$EXECUTION_IMAGE' '$AWS_REGION'"
  parameters=$(jq -nc --arg command "$command" '{commands:[$command]}')
  command_id=$(aws ssm send-command --region "$AWS_REGION" --instance-ids "$instance_id" \
    --document-name AWS-RunShellScript --timeout-seconds 1800 --parameters "$parameters" \
    --query 'Command.CommandId' --output text)
  for _ in {1..360}; do
    state=$(aws ssm get-command-invocation --region "$AWS_REGION" --command-id "$command_id" \
      --instance-id "$instance_id" --query Status --output text 2>/dev/null || true)
    [[ "$state" =~ ^(Success|Failed|Cancelled|TimedOut)$ ]] && break
    sleep 5
  done
  aws ssm get-command-invocation --region "$AWS_REGION" --command-id "$command_id" \
    --instance-id "$instance_id" --query StandardOutputContent --output text || true
  if [[ "$state" != "Success" ]]; then
    aws ssm get-command-invocation --region "$AWS_REGION" --command-id "$command_id" \
      --instance-id "$instance_id" --query StandardErrorContent --output text >&2 || true
    return 1
  fi
}

wait_for_ssm "$worker_id"
wait_for_ssm "$gateway_id"
printf 'Updating the isolated worker...\n'
run_remote "$worker_id" apply worker || die "worker release failed and was rolled back"
printf 'Updating the gateway...\n'
if ! run_remote "$gateway_id" apply gateway; then
  run_remote "$worker_id" rollback worker || true
  die "gateway release failed; gateway and worker rollback was requested"
fi

URL="https://$(awk -F' *= *' '/^domain_name/ {gsub(/\"/, "", $2); print $2}' "$VAR_FILE")"
if ! curl --fail --silent --show-error --retry 18 --retry-delay 5 --retry-all-errors "$URL/ready" >/dev/null; then
  run_remote "$gateway_id" rollback gateway || true
  run_remote "$worker_id" rollback worker || true
  die "public readiness failed; rollback was requested"
fi

update_metadata() {
  local key=$1 value=$2 tmp
  tmp=$(mktemp "${ENV_FILE}.XXXXXX")
  awk -F= -v key="$key" -v value="$value" 'BEGIN {found=0} $1==key {print key "=" value; found=1; next} {print} END {if (!found) print key "=" value}' "$ENV_FILE" >"$tmp"
  chmod 0600 "$tmp"
  mv -f "$tmp" "$ENV_FILE"
}
update_metadata GATEWAY_IMAGE "$GATEWAY_IMAGE"
update_metadata EXECUTION_IMAGE "$EXECUTION_IMAGE"
update_metadata RELEASE_VERSION "$BUILD_TAG"
printf 'Soulacy %s is healthy at %s\n' "$BUILD_TAG" "$URL"
