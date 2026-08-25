#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
REPO_ROOT=$(cd -- "$SCRIPT_DIR/../.." && pwd)
VAR_FILE="$SCRIPT_DIR/terraform.tfvars"
DEPLOYMENT_MODE="${SOULACY_AWS_MODE:-}"
AWS_REGION="${SOULACY_AWS_REGION:-us-east-1}"
DEPLOYMENT_NAME="${SOULACY_AWS_NAME:-soulacy}"
ENVIRONMENT="${SOULACY_AWS_ENVIRONMENT:-production}"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is required"; }
usage() {
  cat <<'USAGE'
Usage: deploy.sh [--mode personal|team|scale] [--var-file PATH]

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
    printf 'Select the Soulacy variant:\n  1) Personal — single operator, embedded storage\n  2) Team — multi-user, PostgreSQL, KMS, NATS and isolated worker\n  3) Scale — Team plus shared Redis and S3 artifacts\n'
    read -r -p 'Choice [1-3]: ' choice
    case "$choice" in 1) DEPLOYMENT_MODE=personal ;; 2) DEPLOYMENT_MODE=team ;; 3) DEPLOYMENT_MODE=scale ;; *) die "invalid variant selection" ;; esac
  else
    die "select a variant with --mode personal|team|scale"
  fi
fi
[[ "$DEPLOYMENT_MODE" =~ ^(personal|team|scale)$ ]] || die "mode must be personal, team, or scale"
for command in aws terraform docker jq openssl cosign curl git; do need "$command"; done
docker buildx version >/dev/null 2>&1 || die "Docker Buildx is required"
[[ -f "$VAR_FILE" ]] || die "Terraform variable file not found: $VAR_FILE (copy terraform.tfvars.example first)"
[[ "$DEPLOYMENT_NAME" =~ ^[a-z][a-z0-9-]{1,20}$ ]] || die "SOULACY_AWS_NAME has an invalid format"

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

printf 'Deploying Soulacy %s (%s variant) to AWS account %s in %s\n' "$BUILD_TAG" "$DEPLOYMENT_MODE" "$ACCOUNT_ID" "$AWS_REGION"

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
[[ -n "$api_key" ]] || api_key="sy_$(openssl rand -hex 32)"
[[ -n "$jwt_secret" ]] || jwt_secret=$(openssl rand -base64 48 | tr -d '\n')

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
  --arg cosign_public_key_b64 "$(b64 "$TMP_DIR/execution-image.pub")" \
  '{api_key:$api_key,jwt_secret:$jwt_secret,oidc_client_secret:$oidc_client_secret,cosign_public_key_b64:$cosign_public_key_b64}' \
  >"$TMP_DIR/bootstrap.json"
ensure_secret "$BOOTSTRAP_SECRET" "$TMP_DIR/bootstrap.json"
if [[ "$DEPLOYMENT_MODE" != "personal" ]]; then ensure_secret "$NATS_SECRET" "$TMP_DIR/nats.json"; fi

terraform -chdir="$SCRIPT_DIR" init -reconfigure \
  -backend-config="bucket=$STATE_BUCKET" -backend-config="key=$STATE_KEY" \
  -backend-config="region=$AWS_REGION" -backend-config="encrypt=true" -backend-config="use_lockfile=true"
terraform -chdir="$SCRIPT_DIR" apply -auto-approve -var-file="$VAR_FILE" \
  -var="aws_region=$AWS_REGION" -var="name=$DEPLOYMENT_NAME" -var="environment=$ENVIRONMENT" -var="deployment_mode=$DEPLOYMENT_MODE" \
  -var="gateway_image=$GATEWAY_IMAGE" -var="execution_image=$EXECUTION_IMAGE" -var="nats_image=$NATS_IMAGE" \
  -var="bootstrap_secret_name=$BOOTSTRAP_SECRET" -var="nats_tls_secret_name=$NATS_SECRET"

cat >"$SCRIPT_DIR/.deployment.env" <<META
AWS_REGION=$AWS_REGION
DEPLOYMENT_NAME=$DEPLOYMENT_NAME
ENVIRONMENT=$ENVIRONMENT
DEPLOYMENT_MODE=$DEPLOYMENT_MODE
STATE_BUCKET=$STATE_BUCKET
STATE_KEY=$STATE_KEY
VAR_FILE=$VAR_FILE
GATEWAY_IMAGE=$GATEWAY_IMAGE
EXECUTION_IMAGE=$EXECUTION_IMAGE
NATS_IMAGE=$NATS_IMAGE
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
printf 'Bootstrap key remains in Secrets Manager secret: %s\n' "$BOOTSTRAP_SECRET"
printf 'Run: %s/status.sh\n' "$SCRIPT_DIR"
