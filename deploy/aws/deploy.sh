#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../.." && pwd)
VAR_FILE="$SCRIPT_DIR/terraform.tfvars"
DEPLOYMENT_MODE="${SOULACY_AWS_MODE:-}"
INFRASTRUCTURE_PROFILE="${SOULACY_AWS_INFRASTRUCTURE_PROFILE:-standard}"
BUDGET_EMAIL="${SOULACY_AWS_BUDGET_EMAIL:-}"
MONTHLY_BUDGET_USD="${SOULACY_AWS_MONTHLY_BUDGET_USD:-180}"
OFF_HOURS_TIMEZONE="${SOULACY_AWS_OFF_HOURS_TIMEZONE:-America/Chicago}"
OFF_HOURS_START="${SOULACY_AWS_OFF_HOURS_START:-8}"
OFF_HOURS_STOP="${SOULACY_AWS_OFF_HOURS_STOP:-22}"
AWS_REGION="${SOULACY_AWS_REGION:-us-east-1}"
DEPLOYMENT_NAME="${SOULACY_AWS_NAME:-soulacy}"
ENVIRONMENT="${SOULACY_AWS_ENVIRONMENT:-}"
INGRESS_MODE="${SOULACY_AWS_INGRESS_MODE:-}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
usage() {
  cat <<'USAGE'
Usage: deploy.sh [--mode personal|team-lite|team|scale] [--var-file PATH]

If --mode is omitted on an interactive terminal, the script asks which Soulacy
variant to install. CI must pass --mode or set SOULACY_AWS_MODE.
USAGE
}
while (($#)); do
  case "$1" in
    --mode) [[ $# -ge 2 ]] || die "--mode requires a value"; DEPLOYMENT_MODE="$2"; shift 2 ;;
    --var-file) [[ $# -ge 2 ]] || die "--var-file requires a path"; VAR_FILE="$2"; shift 2 ;;
    -h|--help) usage; exit 0 ;;
    *) die "unknown argument: $1" ;;
  esac
done
if [[ -z "$DEPLOYMENT_MODE" ]]; then
  if [[ -t 0 ]]; then
    printf 'Select the Soulacy variant:\n  1) Personal — single operator, embedded storage\n  2) Team Lite — one-month small-team pilot targeted below $200 of AWS infrastructure\n  3) Team — multi-user, PostgreSQL, KMS, NATS and isolated worker\n  4) Scale — Team plus shared Redis and S3 artifacts\n'
    read -r -p 'Choice [1-4]: ' choice
    case "$choice" in 1) DEPLOYMENT_MODE=personal ;; 2) DEPLOYMENT_MODE=team-lite ;; 3) DEPLOYMENT_MODE=team ;; 4) DEPLOYMENT_MODE=scale ;; *) die "invalid variant selection" ;; esac
  else
    die "select a variant with --mode personal|team-lite|team|scale"
  fi
fi
[[ "$DEPLOYMENT_MODE" =~ ^(personal|team-lite|team|scale)$ ]] || die "mode must be personal, team-lite, team, or scale"
INSTALL_VARIANT="$DEPLOYMENT_MODE"
if [[ "$INSTALL_VARIANT" == "team-lite" ]]; then
  DEPLOYMENT_MODE=team
  INFRASTRUCTURE_PROFILE=budget
fi
if [[ -z "$INGRESS_MODE" ]]; then
  if [[ "$INFRASTRUCTURE_PROFILE" == "budget" ]]; then INGRESS_MODE=cloudflare_tunnel; else INGRESS_MODE=alb; fi
fi
[[ "$INGRESS_MODE" =~ ^(alb|cloudflare_tunnel)$ ]] || die "ingress mode must be alb or cloudflare_tunnel"
if [[ "$INGRESS_MODE" == "cloudflare_tunnel" && "$DEPLOYMENT_MODE" == "personal" ]]; then
  die "Cloudflare Tunnel ingress is supported for Team and Scale; Personal uses the standard ALB edge"
fi
if [[ -z "$ENVIRONMENT" ]]; then
  if [[ "$INFRASTRUCTURE_PROFILE" == "budget" ]]; then ENVIRONMENT=pilot; else ENVIRONMENT=production; fi
fi
[[ "$INFRASTRUCTURE_PROFILE" =~ ^(standard|budget)$ ]] || die "infrastructure profile must be standard or budget"
if [[ "$INFRASTRUCTURE_PROFILE" == "budget" && "$DEPLOYMENT_MODE" != "team" ]]; then
  die "the budget infrastructure profile is supported only by Team Lite"
fi
if [[ "$INFRASTRUCTURE_PROFILE" == "budget" && -z "$BUDGET_EMAIL" ]]; then
  if [[ -t 0 ]]; then
    read -r -p 'Email for AWS spend alerts: ' BUDGET_EMAIL
  else
    die "Team Lite requires SOULACY_AWS_BUDGET_EMAIL for spend alerts"
  fi
fi
if [[ "$INFRASTRUCTURE_PROFILE" == "budget" ]]; then
  [[ "$BUDGET_EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] || die "invalid AWS budget alert email"
  [[ "$MONTHLY_BUDGET_USD" =~ ^[0-9]+([.][0-9]+)?$ ]] || die "SOULACY_AWS_MONTHLY_BUDGET_USD must be numeric"
  [[ "$OFF_HOURS_TIMEZONE" =~ ^[A-Za-z_]+/[A-Za-z0-9_+.-]+$ ]] || die "invalid SOULACY_AWS_OFF_HOURS_TIMEZONE"
  [[ "$OFF_HOURS_START" =~ ^([0-9]|1[0-9]|2[0-3])$ ]] || die "SOULACY_AWS_OFF_HOURS_START must be an hour from 0 to 23"
  [[ "$OFF_HOURS_STOP" =~ ^([0-9]|1[0-9]|2[0-3])$ ]] || die "SOULACY_AWS_OFF_HOURS_STOP must be an hour from 0 to 23"
fi
for command in aws terraform docker jq openssl cosign curl git; do need "$command"; done
docker buildx version >/dev/null 2>&1 || die "Docker Buildx is required"
if [[ "$VAR_FILE" != /* ]]; then
  var_file_dir=$(cd -- "$(dirname -- "$VAR_FILE")" 2>/dev/null && pwd) || \
    die "Terraform variable file directory not found: $(dirname -- "$VAR_FILE")"
  VAR_FILE="$var_file_dir/$(basename -- "$VAR_FILE")"
fi
[[ -f "$VAR_FILE" ]] || die "Terraform variable file not found: $VAR_FILE (copy terraform.tfvars.example first)"
[[ "$DEPLOYMENT_NAME" =~ ^[a-z][a-z0-9-]{1,20}$ ]] || die "SOULACY_AWS_NAME has an invalid format"
[[ "$ENVIRONMENT" =~ ^[a-z][a-z0-9-]{1,20}$ ]] || die "SOULACY_AWS_ENVIRONMENT has an invalid format"

# The AWS CLI receives --region explicitly below, but AWS-backed helpers such
# as cosign's awskms signer use the SDK environment instead. Export both
# conventional variables so every AWS client resolves the same endpoint.
export AWS_REGION
export AWS_DEFAULT_REGION="${AWS_DEFAULT_REGION:-$AWS_REGION}"

# `aws login` profiles use login_session, which the AWS CLI understands but
# older Terraform AWS providers may not. Bridge that profile to standard,
# short-lived environment credentials for this process only. Nothing is
# written to disk and an already configured environment always wins.
if [[ -n "${AWS_PROFILE:-}" && -z "${AWS_ACCESS_KEY_ID:-}" ]]; then
  session_credentials=$(aws configure export-credentials --profile "$AWS_PROFILE" --format process 2>/dev/null) || \
    die "could not export short-lived credentials from AWS profile $AWS_PROFILE; run aws login --profile $AWS_PROFILE"
  export AWS_ACCESS_KEY_ID="$(jq -er '.AccessKeyId' <<<"$session_credentials")"
  export AWS_SECRET_ACCESS_KEY="$(jq -er '.SecretAccessKey' <<<"$session_credentials")"
  export AWS_SESSION_TOKEN="$(jq -er '.SessionToken' <<<"$session_credentials")"
  unset session_credentials
fi

ACCOUNT_ID=$(aws sts get-caller-identity --query Account --output text)
REGISTRY="$ACCOUNT_ID.dkr.ecr.$AWS_REGION.amazonaws.com"
GATEWAY_REPO="$DEPLOYMENT_NAME-gateway"
EXECUTION_REPO="$DEPLOYMENT_NAME-execution"
BOOTSTRAP_SECRET="$DEPLOYMENT_NAME/$ENVIRONMENT/bootstrap"
NATS_SECRET="$DEPLOYMENT_NAME/$ENVIRONMENT/nats-tls"
STATE_BUCKET="$ACCOUNT_ID-$AWS_REGION-$DEPLOYMENT_NAME-tfstate"
STATE_KEY="$ENVIRONMENT/terraform.tfstate"
SIGNING_ALIAS="alias/$DEPLOYMENT_NAME-$ENVIRONMENT-execution-signing"
BUILD_TAG=$(git -C "$REPO_ROOT" rev-parse --short=12 HEAD)
if [[ -n "$(git -C "$REPO_ROOT" status --porcelain)" ]]; then
  if [[ "${SOULACY_AWS_ALLOW_DIRTY:-}" != "1" ]]; then
    die "the repository has uncommitted changes; commit them first or explicitly set SOULACY_AWS_ALLOW_DIRTY=1"
  fi
  BUILD_TAG="$BUILD_TAG-dirty-$(date -u +%Y%m%d%H%M%S)"
fi
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/soulacy-aws.XXXXXX")
cleanup() { rm -rf -- "$TMP_DIR"; }
trap cleanup EXIT

printf 'Deploying Soulacy %s (%s variant, %s infrastructure) to AWS account %s in %s\n' "$BUILD_TAG" "$INSTALL_VARIANT" "$INFRASTRUCTURE_PROFILE" "$ACCOUNT_ID" "$AWS_REGION"
if [[ "$INFRASTRUCTURE_PROFILE" == "budget" ]]; then
  printf '%s\n' 'Team Lite target: about $55-$115 for 730 always-on hours at low traffic in us-east-1, before LLM/API use; AWS credits and prices are not guarantees.'
  printf '%s\n' 'Availability tradeoff: PostgreSQL is Single-AZ and compute is burstable. Destroy it promptly after the pilot.'
fi

ensure_state_bucket() {
  if ! aws s3api head-bucket --bucket "$STATE_BUCKET" >/dev/null 2>&1; then
    if [[ "$AWS_REGION" == "us-east-1" ]]; then
      aws s3api create-bucket --bucket "$STATE_BUCKET" --region "$AWS_REGION" >/dev/null
    else
      aws s3api create-bucket --bucket "$STATE_BUCKET" --region "$AWS_REGION" \
        --create-bucket-configuration "LocationConstraint=$AWS_REGION" >/dev/null
    fi
  fi
  aws s3api put-public-access-block --bucket "$STATE_BUCKET" --public-access-block-configuration \
    'BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true'
  aws s3api put-bucket-versioning --bucket "$STATE_BUCKET" --versioning-configuration Status=Enabled
  aws s3api put-bucket-encryption --bucket "$STATE_BUCKET" --server-side-encryption-configuration \
    '{"Rules":[{"ApplyServerSideEncryptionByDefault":{"SSEAlgorithm":"AES256"},"BucketKeyEnabled":true}]}'
}

ensure_ecr_repo() {
  local repo="$1"
  aws ecr describe-repositories --region "$AWS_REGION" --repository-names "$repo" >/dev/null 2>&1 || \
    aws ecr create-repository --region "$AWS_REGION" --repository-name "$repo" \
      --image-scanning-configuration scanOnPush=true --image-tag-mutability IMMUTABLE >/dev/null
  aws ecr put-lifecycle-policy --region "$AWS_REGION" --repository-name "$repo" --lifecycle-policy-text \
    '{"rules":[{"rulePriority":1,"description":"Keep 20 release images","selection":{"tagStatus":"any","countType":"imageCountMoreThan","countNumber":20},"action":{"type":"expire"}}]}' >/dev/null
}

resolve_digest() {
  local image="$1" digest
  digest=$(docker buildx imagetools inspect "$image" | awk '/^Digest:/ {print $2; exit}')
  [[ "$digest" == sha256:* ]] || die "could not resolve immutable digest for $image"
  printf '%s@%s' "$image" "$digest"
}

ensure_secret() {
  local name="$1" json_file="$2"
  if aws secretsmanager describe-secret --region "$AWS_REGION" --secret-id "$name" >/dev/null 2>&1; then
    aws secretsmanager put-secret-value --region "$AWS_REGION" --secret-id "$name" --secret-string "file://$json_file" >/dev/null
  else
    aws secretsmanager create-secret --region "$AWS_REGION" --name "$name" --secret-string "file://$json_file" >/dev/null
  fi
}

existing_bootstrap='{}'
if aws secretsmanager describe-secret --region "$AWS_REGION" --secret-id "$BOOTSTRAP_SECRET" >/dev/null 2>&1; then
  existing_bootstrap=$(aws secretsmanager get-secret-value --region "$AWS_REGION" --secret-id "$BOOTSTRAP_SECRET" --query SecretString --output text)
fi
api_key=$(jq -r '.api_key // empty' <<<"$existing_bootstrap")
jwt_secret=$(jq -r '.jwt_secret // empty' <<<"$existing_bootstrap")
oidc_secret="${SOULACY_AWS_OIDC_CLIENT_SECRET:-$(jq -r '.oidc_client_secret // empty' <<<"$existing_bootstrap")}"
tunnel_token="${SOULACY_CLOUDFLARE_TUNNEL_TOKEN:-$(jq -r '.cloudflare_tunnel_token // empty' <<<"$existing_bootstrap")}"
[[ -n "$api_key" ]] || api_key="sy_$(openssl rand -hex 32)"
[[ -n "$jwt_secret" ]] || jwt_secret=$(openssl rand -base64 48 | tr -d '\n')
if [[ "$INGRESS_MODE" == "cloudflare_tunnel" && -z "$tunnel_token" ]]; then
  die "Cloudflare Tunnel ingress requires SOULACY_CLOUDFLARE_TUNNEL_TOKEN; use team-lite-quickstart.sh to create it automatically"
fi

ensure_state_bucket
ensure_ecr_repo "$GATEWAY_REPO"
ensure_ecr_repo "$EXECUTION_REPO"
aws ecr get-login-password --region "$AWS_REGION" | docker login --username AWS --password-stdin "$REGISTRY"

printf 'Building and pushing immutable gateway image...\n'
if ! aws ecr describe-images --region "$AWS_REGION" --repository-name "$GATEWAY_REPO" --image-ids imageTag="$BUILD_TAG" >/dev/null 2>&1; then
  docker buildx build --platform linux/amd64 --provenance=false --push \
    --build-arg "VERSION=$BUILD_TAG" -t "$REGISTRY/$GATEWAY_REPO:$BUILD_TAG" "$REPO_ROOT"
fi
gateway_digest=$(aws ecr describe-images --region "$AWS_REGION" --repository-name "$GATEWAY_REPO" \
  --image-ids imageTag="$BUILD_TAG" --query 'imageDetails[0].imageDigest' --output text)
GATEWAY_IMAGE="$REGISTRY/$GATEWAY_REPO@$gateway_digest"

PYTHON_IMAGE=$(resolve_digest "python:3.12-slim")
printf 'Building and pushing minimal execution image from %s...\n' "$PYTHON_IMAGE"
if ! aws ecr describe-images --region "$AWS_REGION" --repository-name "$EXECUTION_REPO" --image-ids imageTag="$BUILD_TAG" >/dev/null 2>&1; then
  docker buildx build --platform linux/amd64 --provenance=false --push \
    --build-arg "PYTHON_IMAGE=$PYTHON_IMAGE" -f "$SCRIPT_DIR/Dockerfile.execution" \
    -t "$REGISTRY/$EXECUTION_REPO:$BUILD_TAG" "$SCRIPT_DIR"
fi
execution_digest=$(aws ecr describe-images --region "$AWS_REGION" --repository-name "$EXECUTION_REPO" \
  --image-ids imageTag="$BUILD_TAG" --query 'imageDetails[0].imageDigest' --output text)
EXECUTION_IMAGE="$REGISTRY/$EXECUTION_REPO@$execution_digest"
NATS_IMAGE="nats@sha256:0000000000000000000000000000000000000000000000000000000000000000"
if [[ "$DEPLOYMENT_MODE" != "personal" ]]; then NATS_IMAGE=$(resolve_digest "nats:2.14.5-alpine"); fi
CLOUDFLARED_IMAGE="cloudflare/cloudflared@sha256:0000000000000000000000000000000000000000000000000000000000000000"
if [[ "$INGRESS_MODE" == "cloudflare_tunnel" ]]; then CLOUDFLARED_IMAGE=$(resolve_digest "cloudflare/cloudflared:latest"); fi

signing_key_id=$(aws kms list-aliases --region "$AWS_REGION" --query "Aliases[?AliasName=='$SIGNING_ALIAS'].TargetKeyId | [0]" --output text)
if [[ -z "$signing_key_id" || "$signing_key_id" == "None" ]]; then
  signing_key_id=$(aws kms create-key --region "$AWS_REGION" --description "Soulacy execution image signing" \
    --key-usage SIGN_VERIFY --key-spec ECC_NIST_P256 --query KeyMetadata.KeyId --output text)
  aws kms create-alias --region "$AWS_REGION" --alias-name "$SIGNING_ALIAS" --target-key-id "$signing_key_id"
fi
signing_arn=$(aws kms describe-key --region "$AWS_REGION" --key-id "$signing_key_id" --query KeyMetadata.Arn --output text)
signing_uri="awskms:///$signing_arn"
printf 'Signing execution image with AWS KMS...\n'
cosign sign --yes --key "$signing_uri" "$EXECUTION_IMAGE"
cosign public-key --key "$signing_uri" >"$TMP_DIR/execution-image.pub"

if [[ "$DEPLOYMENT_MODE" != "personal" ]]; then
  if aws secretsmanager describe-secret --region "$AWS_REGION" --secret-id "$NATS_SECRET" >/dev/null 2>&1; then
    aws secretsmanager get-secret-value --region "$AWS_REGION" --secret-id "$NATS_SECRET" --query SecretString --output text >"$TMP_DIR/nats.json"
  else
  openssl ecparam -name prime256v1 -genkey -noout -out "$TMP_DIR/ca-key.pem"
  openssl req -x509 -new -sha256 -days 3650 -key "$TMP_DIR/ca-key.pem" -out "$TMP_DIR/ca.pem" -subj "/CN=Soulacy NATS Private CA"
  issue_cert() {
    local name="$1" cn="$2" san="$3"
    openssl ecparam -name prime256v1 -genkey -noout -out "$TMP_DIR/$name-key.pem"
    openssl req -new -sha256 -key "$TMP_DIR/$name-key.pem" -out "$TMP_DIR/$name.csr" -subj "/CN=$cn"
    printf 'basicConstraints=CA:FALSE\nkeyUsage=digitalSignature,keyEncipherment\nextendedKeyUsage=%s\nsubjectAltName=%s\n' \
      "$([[ "$name" == server ]] && printf serverAuth || printf clientAuth)" "$san" >"$TMP_DIR/$name.ext"
    openssl x509 -req -sha256 -days 825 -in "$TMP_DIR/$name.csr" -CA "$TMP_DIR/ca.pem" -CAkey "$TMP_DIR/ca-key.pem" \
      -CAcreateserial -out "$TMP_DIR/$name.pem" -extfile "$TMP_DIR/$name.ext" >/dev/null 2>&1
  }
    issue_cert server nats.soulacy.internal DNS:nats.soulacy.internal
    issue_cert gateway gateway DNS:gateway.soulacy.internal
    issue_cert worker worker DNS:worker.soulacy.internal

    b64() { openssl base64 -A -in "$1"; }
    jq -n --arg ca_b64 "$(b64 "$TMP_DIR/ca.pem")" \
      --arg server_cert_b64 "$(b64 "$TMP_DIR/server.pem")" --arg server_key_b64 "$(b64 "$TMP_DIR/server-key.pem")" \
      --arg gateway_cert_b64 "$(b64 "$TMP_DIR/gateway.pem")" --arg gateway_key_b64 "$(b64 "$TMP_DIR/gateway-key.pem")" \
      --arg worker_cert_b64 "$(b64 "$TMP_DIR/worker.pem")" --arg worker_key_b64 "$(b64 "$TMP_DIR/worker-key.pem")" \
      '{$ca_b64,$server_cert_b64,$server_key_b64,$gateway_cert_b64,$gateway_key_b64,$worker_cert_b64,$worker_key_b64}' \
      >"$TMP_DIR/nats.json"
  fi
fi

b64() { openssl base64 -A -in "$1"; }
jq -n --arg api_key "$api_key" --arg jwt_secret "$jwt_secret" --arg oidc_client_secret "$oidc_secret" \
  --arg cloudflare_tunnel_token "$tunnel_token" \
  --arg cosign_public_key_b64 "$(b64 "$TMP_DIR/execution-image.pub")" \
  '{api_key:$api_key,jwt_secret:$jwt_secret,oidc_client_secret:$oidc_client_secret,cloudflare_tunnel_token:$cloudflare_tunnel_token,cosign_public_key_b64:$cosign_public_key_b64}' \
  >"$TMP_DIR/bootstrap.json"
ensure_secret "$BOOTSTRAP_SECRET" "$TMP_DIR/bootstrap.json"
if [[ "$DEPLOYMENT_MODE" != "personal" ]]; then ensure_secret "$NATS_SECRET" "$TMP_DIR/nats.json"; fi

terraform -chdir="$SCRIPT_DIR" init -reconfigure \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" -backend-config="use_lockfile=true"
terraform -chdir="$SCRIPT_DIR" apply -auto-approve -var-file="$VAR_FILE" \
  -var="aws_region=$AWS_REGION" -var="name=$DEPLOYMENT_NAME" -var="environment=$ENVIRONMENT" -var="deployment_mode=$DEPLOYMENT_MODE" \
  -var="infrastructure_profile=$INFRASTRUCTURE_PROFILE" -var="budget_alert_email=$BUDGET_EMAIL" -var="monthly_budget_usd=$MONTHLY_BUDGET_USD" \
  -var="off_hours_timezone=$OFF_HOURS_TIMEZONE" -var="off_hours_start_hour=$OFF_HOURS_START" -var="off_hours_stop_hour=$OFF_HOURS_STOP" \
  -var="ingress_mode=$INGRESS_MODE" -var="gateway_image=$GATEWAY_IMAGE" -var="execution_image=$EXECUTION_IMAGE" -var="nats_image=$NATS_IMAGE" -var="cloudflared_image=$CLOUDFLARED_IMAGE" \
  -var="bootstrap_secret_name=$BOOTSTRAP_SECRET" -var="nats_tls_secret_name=$NATS_SECRET"

cat >"$SCRIPT_DIR/.deployment.env" <<META
AWS_PROFILE=${AWS_PROFILE:-}
AWS_REGION=$AWS_REGION
DEPLOYMENT_NAME=$DEPLOYMENT_NAME
ENVIRONMENT=$ENVIRONMENT
DEPLOYMENT_MODE=$DEPLOYMENT_MODE
INSTALL_VARIANT=$INSTALL_VARIANT
INFRASTRUCTURE_PROFILE=$INFRASTRUCTURE_PROFILE
INGRESS_MODE=$INGRESS_MODE
BUDGET_EMAIL=$BUDGET_EMAIL
MONTHLY_BUDGET_USD=$MONTHLY_BUDGET_USD
OFF_HOURS_TIMEZONE=$OFF_HOURS_TIMEZONE
OFF_HOURS_START=$OFF_HOURS_START
OFF_HOURS_STOP=$OFF_HOURS_STOP
STATE_BUCKET=$STATE_BUCKET
STATE_KEY=$STATE_KEY
VAR_FILE=$VAR_FILE
GATEWAY_IMAGE=$GATEWAY_IMAGE
EXECUTION_IMAGE=$EXECUTION_IMAGE
NATS_IMAGE=$NATS_IMAGE
CLOUDFLARED_IMAGE=$CLOUDFLARED_IMAGE
BOOTSTRAP_SECRET=$BOOTSTRAP_SECRET
NATS_SECRET=$NATS_SECRET
META
chmod 0600 "$SCRIPT_DIR/.deployment.env"

URL=$(terraform -chdir="$SCRIPT_DIR" output -raw url)
printf 'Waiting for %s to become healthy...\n' "$URL"
if ! curl --fail --silent --show-error --retry 90 --retry-delay 10 --retry-all-errors "$URL/ready" >/dev/null; then
  printf 'Deployment created, but health did not become ready. Run %s/status.sh for diagnostics.\n' "$SCRIPT_DIR" >&2
  exit 1
fi

printf '\nSoulacy is ready: %s\n' "$URL"
if [[ "$INFRASTRUCTURE_PROFILE" == "budget" ]]; then
  printf 'Team Lite is running as application mode Team. Review AWS Billing daily and destroy the stack after the pilot.\n'
fi
printf 'Bootstrap key remains in Secrets Manager secret: %s\n' "$BOOTSTRAP_SECRET"
printf 'Run: %s/status.sh\n' "$SCRIPT_DIR"
