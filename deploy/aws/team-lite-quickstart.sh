#!/usr/bin/env bash
set -Eeuo pipefail

SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
VAR_FILE="${SOULACY_AWS_VAR_FILE:-$SCRIPT_DIR/terraform.tfvars}"
CF_API="https://api.cloudflare.com/client/v4"

die() { printf 'error: %s\n' "$*" >&2; exit 1; }
say() { printf '\n==> %s\n' "$*"; }
has() { command -v "$1" >/dev/null 2>&1; }
is_interactive() { [[ -t 0 && -t 1 ]]; }
usage() {
  cat <<'USAGE'
Usage: team-lite-quickstart.sh

Guided Team Lite deployment from macOS. The wizard:
  * selects and verifies an AWS CLI profile;
  * creates or reuses a Route 53 child zone;
  * delegates that child zone from Cloudflare;
  * generates terraform.tfvars for Google OIDC; and
  * runs the normal signed, containerized Team Lite deployment.

Optional environment inputs for automation:
  AWS_PROFILE                       AWS CLI profile name
  SOULACY_AWS_BUDGET_EMAIL          AWS Budget notification email
  SOULACY_ROOT_DOMAIN               Cloudflare zone (default: soulac.io)
  SOULACY_SUBDOMAIN                 Child label (default: team)
  CLOUDFLARE_API_TOKEN              Zone:Read and DNS:Edit token
  GOOGLE_OIDC_CLIENT_ID             Google web OAuth client ID
  GOOGLE_OIDC_CLIENT_SECRET         Google web OAuth client secret
  SOULACY_AWS_REGION                AWS region (default: us-east-1)
  SOULACY_AWS_OFF_HOURS_TIMEZONE    IANA timezone (default: detected/Chicago)
  SOULACY_AWS_VAR_FILE              Generated tfvars path (advanced/testing)
  SOULACY_QUICKSTART_CONFIGURE_ONLY Stop after DNS and configuration

Secrets are kept in memory. The Cloudflare token is never persisted; the
Google secret is passed to deploy.sh and written directly to AWS Secrets
Manager.
USAGE
}

[[ $# -eq 0 ]] || {
  [[ $# -eq 1 && ( "$1" == "-h" || "$1" == "--help" ) ]] && { usage; exit 0; }
  usage >&2
  exit 1
}

prompt() {
  local target="$1" label="$2" default="${3:-}" value
  value="${!target:-}"
  if [[ -z "$value" ]]; then
    is_interactive || die "$target is required in non-interactive mode"
    if [[ -n "$default" ]]; then
      read -r -p "$label [$default]: " value
      value="${value:-$default}"
    else
      read -r -p "$label: " value
    fi
  fi
  [[ -n "$value" ]] || die "$label is required"
  printf -v "$target" '%s' "$value"
}

prompt_secret() {
  local target="$1" label="$2" value
  value="${!target:-}"
  if [[ -z "$value" ]]; then
    is_interactive || die "$target is required in non-interactive mode"
    read -r -s -p "$label: " value
    printf '\n'
  fi
  [[ -n "$value" ]] || die "$label is required"
  printf -v "$target" '%s' "$value"
}

confirm() {
  local label="$1" answer
  is_interactive || return 1
  read -r -p "$label [y/N]: " answer
  [[ "$answer" =~ ^[Yy]$ ]]
}

install_prerequisites() {
  local missing=() command
  for command in aws terraform docker jq openssl cosign curl git; do
    has "$command" || missing+=("$command")
  done
  ((${#missing[@]} == 0)) && return

  [[ "$(uname -s)" == "Darwin" ]] || die "missing prerequisites: ${missing[*]}"
  has brew || die "missing prerequisites (${missing[*]}) and Homebrew is unavailable: https://brew.sh"
  confirm "Install missing deployment tools with Homebrew?" || die "missing prerequisites: ${missing[*]}"

  local formulae=()
  for command in "${missing[@]}"; do
    case "$command" in
      aws) formulae+=(awscli) ;;
      terraform|jq|openssl|cosign|git) formulae+=("$command") ;;
      docker) ;;
      curl) formulae+=(curl) ;;
    esac
  done
  ((${#formulae[@]} == 0)) || brew install "${formulae[@]}"
  if ! has docker; then
    brew install --cask docker
  fi
}

ensure_docker() {
  if ! docker info >/dev/null 2>&1; then
    if [[ "$(uname -s)" == "Darwin" ]] && has open; then
      say "Starting Docker Desktop"
      open -a Docker
      local deadline=$((SECONDS + 180))
      until docker info >/dev/null 2>&1; do
        ((SECONDS < deadline)) || die "Docker Desktop did not become ready within three minutes"
        sleep 2
      done
    else
      die "Docker is installed but the daemon is not running"
    fi
  fi
  docker buildx version >/dev/null 2>&1 || die "Docker Buildx is required"
}

configure_aws_profile() {
  local profiles default_profile
  profiles=$(aws configure list-profiles 2>/dev/null || true)
  default_profile="${AWS_PROFILE:-}"
  if [[ -z "$default_profile" ]]; then
    if grep -qx default <<<"$profiles"; then default_profile=default; else default_profile=soulacy-pilot; fi
  fi
  prompt AWS_PROFILE "AWS CLI profile" "$default_profile"
  [[ "$AWS_PROFILE" =~ ^[A-Za-z0-9._-]+$ ]] || die "AWS profile contains unsupported characters"
  export AWS_PROFILE

  if ! aws sts get-caller-identity >/dev/null 2>&1; then
    if grep -qx "$AWS_PROFILE" <<<"$profiles"; then
      say "Signing in to AWS profile $AWS_PROFILE"
      aws sso login --profile "$AWS_PROFILE" || true
    fi
  fi
  if ! aws sts get-caller-identity >/dev/null 2>&1; then
    printf 'AWS profile %s is not authenticated. Configure an IAM Identity Center profile, then rerun:\n' "$AWS_PROFILE" >&2
    printf '  aws configure sso --profile %s\n' "$AWS_PROFILE" >&2
    die "AWS authentication is required"
  fi
}

cf_call() {
  local method="$1" path="$2" body="${3:-}" response
  if [[ -n "$body" ]]; then
    response=$(printf 'header = "Authorization: Bearer %s"\n' "$CF_TOKEN" | \
      curl --silent --show-error --config - --request "$method" \
        --header 'Content-Type: application/json' --data-binary "$body" "$CF_API$path")
  else
    response=$(printf 'header = "Authorization: Bearer %s"\n' "$CF_TOKEN" | \
      curl --silent --show-error --config - --request "$method" "$CF_API$path")
  fi
  jq -e '.success == true' >/dev/null <<<"$response" || {
    jq -r '.errors[]? | "Cloudflare error \(.code): \(.message)"' <<<"$response" >&2
    return 1
  }
  printf '%s' "$response"
}

ensure_cloudflare_zone() {
  local response
  response=$(cf_call GET "/zones?name=$ROOT_DOMAIN&status=active&per_page=2") || \
    die "Cloudflare token validation failed; it needs Zone:Read and DNS:Edit for $ROOT_DOMAIN"
  CF_ZONE_ID=$(jq -r --arg name "$ROOT_DOMAIN" '.result[] | select(.name == $name) | .id' <<<"$response" | head -n1)
  [[ -n "$CF_ZONE_ID" ]] || die "$ROOT_DOMAIN is not an active zone visible to this Cloudflare token"
}

ensure_route53_child_zone() {
  local zones create_json
  zones=$(aws route53 list-hosted-zones-by-name --dns-name "$FQDN" --max-items 10 --output json)
  ROUTE53_ZONE_ID=$(jq -r --arg fqdn "$FQDN." \
    '.HostedZones[] | select(.Name == $fqdn and .Config.PrivateZone == false) | .Id' <<<"$zones" | head -n1)
  ROUTE53_ZONE_ID="${ROUTE53_ZONE_ID#/hostedzone/}"
  if [[ -z "$ROUTE53_ZONE_ID" ]]; then
    say "Creating Route 53 child zone $FQDN"
    create_json=$(aws route53 create-hosted-zone --name "$FQDN" \
      --caller-reference "soulacy-$(date -u +%Y%m%dT%H%M%SZ)-$$" \
      --hosted-zone-config "Comment=Soulacy Team Lite delegated child zone,PrivateZone=false" --output json)
    ROUTE53_ZONE_ID=$(jq -r '.HostedZone.Id | sub("^/hostedzone/"; "")' <<<"$create_json")
  else
    say "Reusing Route 53 child zone $FQDN ($ROUTE53_ZONE_ID)"
  fi
  ROUTE53_NAMESERVERS=()
  while IFS= read -r server; do
    [[ -n "$server" ]] && ROUTE53_NAMESERVERS+=("$server")
  done < <(aws route53 get-hosted-zone --id "$ROUTE53_ZONE_ID" \
    --query 'DelegationSet.NameServers' --output json | jq -r '.[]' | sed 's/[.]$//' | sort)
  ((${#ROUTE53_NAMESERVERS[@]} >= 2)) || die "Route 53 returned no usable delegation nameservers"
}

ensure_cloudflare_delegation() {
  local records non_ns current expected server body
  records=$(cf_call GET "/zones/$CF_ZONE_ID/dns_records?name=$FQDN&per_page=100") || die "could not inspect Cloudflare DNS"
  non_ns=$(jq -r '.result[] | select(.type != "NS") | "\(.type) \(.name) -> \(.content)"' <<<"$records")
  [[ -z "$non_ns" ]] || die "Cloudflare already has non-NS records at $FQDN; move them before delegation: $non_ns"

  current=$(jq -r '.result[] | select(.type == "NS") | (.content | ascii_downcase | rtrimstr("."))' <<<"$records" | sort)
  expected=$(printf '%s\n' "${ROUTE53_NAMESERVERS[@]}" | tr '[:upper:]' '[:lower:]' | sort)
  if [[ -n "$current" && "$current" != "$expected" ]]; then
    printf 'Existing Cloudflare NS records for %s point elsewhere:\n%s\n' "$FQDN" "$current" >&2
    die "refusing to replace an existing delegation automatically"
  fi

  for server in "${ROUTE53_NAMESERVERS[@]}"; do
    if ! jq -e --arg content "$server" \
      '.result[] | select(.type == "NS" and ((.content | ascii_downcase | rtrimstr(".")) == ($content | ascii_downcase)))' \
      >/dev/null <<<"$records"; then
      body=$(jq -n --arg name "$FQDN" --arg content "$server" \
        '{type:"NS", name:$name, content:$content, ttl:3600}')
      cf_call POST "/zones/$CF_ZONE_ID/dns_records" "$body" >/dev/null || die "failed to create Cloudflare delegation record"
    fi
  done
  say "Cloudflare now delegates $FQDN to Route 53"
}

write_tfvars() {
  local backup=""
  if [[ -f "$VAR_FILE" ]]; then
    backup="$VAR_FILE.backup.$(date -u +%Y%m%dT%H%M%SZ)"
    cp -p "$VAR_FILE" "$backup"
  fi
  local tmp
  tmp=$(mktemp "${TMPDIR:-/tmp}/soulacy-tfvars.XXXXXX")
  jq -Rnr \
    --arg region "$AWS_REGION" --arg domain "$FQDN" --arg zone "$ROUTE53_ZONE_ID" \
    --arg client "$GOOGLE_CLIENT_ID" --arg timezone "$OFF_HOURS_TIMEZONE" '
      "# Generated by team-lite-quickstart.sh — contains no secrets\n" +
      "aws_region = " + ($region|tojson) + "\n" +
      "name = \"soulacy\"\n" +
      "environment = \"pilot\"\n" +
      "deployment_mode = \"team\"\n" +
      "infrastructure_profile = \"budget\"\n" +
      "domain_name = " + ($domain|tojson) + "\n" +
      "route53_zone_id = " + ($zone|tojson) + "\n" +
      "oidc_issuer = \"https://accounts.google.com\"\n" +
      "oidc_client_id = " + ($client|tojson) + "\n" +
      "off_hours_timezone = " + ($timezone|tojson) + "\n" +
      "enable_deletion_protection = true\n"
    ' >"$tmp"
  chmod 0600 "$tmp"
  mv "$tmp" "$VAR_FILE"
  [[ -z "$backup" ]] || printf 'Previous Terraform variables backed up to %s\n' "$backup"
}

install_prerequisites
ensure_docker
configure_aws_profile

AWS_REGION="${SOULACY_AWS_REGION:-us-east-1}"
ROOT_DOMAIN="${SOULACY_ROOT_DOMAIN:-}"
SUBDOMAIN="${SOULACY_SUBDOMAIN:-}"
BUDGET_EMAIL="${SOULACY_AWS_BUDGET_EMAIL:-}"
CF_TOKEN="${CLOUDFLARE_API_TOKEN:-}"
GOOGLE_CLIENT_ID="${GOOGLE_OIDC_CLIENT_ID:-}"
GOOGLE_CLIENT_SECRET="${GOOGLE_OIDC_CLIENT_SECRET:-}"
OFF_HOURS_TIMEZONE="${SOULACY_AWS_OFF_HOURS_TIMEZONE:-}"

prompt ROOT_DOMAIN "Cloudflare root domain" "soulac.io"
prompt SUBDOMAIN "Soulacy subdomain label" "team"
prompt BUDGET_EMAIL "AWS budget-alert email"
[[ "$ROOT_DOMAIN" =~ ^([a-z0-9-]+[.])+[a-z]{2,63}$ ]] || die "invalid root domain"
[[ "$SUBDOMAIN" =~ ^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$ ]] || die "subdomain must be one DNS label"
FQDN="$SUBDOMAIN.$ROOT_DOMAIN"
[[ "$BUDGET_EMAIL" =~ ^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$ ]] || die "invalid budget email"

if [[ -z "$OFF_HOURS_TIMEZONE" && -L /etc/localtime ]]; then
  OFF_HOURS_TIMEZONE=$(readlink /etc/localtime | sed -n 's#.*zoneinfo/##p')
fi
OFF_HOURS_TIMEZONE="${OFF_HOURS_TIMEZONE:-America/Chicago}"

say "Cloudflare authorization"
printf 'Create a scoped token with Zone:Read and DNS:Edit for %s.\n' "$ROOT_DOMAIN"
if is_interactive && [[ -z "$CF_TOKEN" ]] && [[ "$(uname -s)" == "Darwin" ]]; then
  open 'https://dash.cloudflare.com/profile/api-tokens' >/dev/null 2>&1 || true
fi
prompt_secret CF_TOKEN "Cloudflare API token"
ensure_cloudflare_zone
ensure_route53_child_zone
ensure_cloudflare_delegation

say "Google OIDC"
printf 'Create a Google OAuth client of type Web application with:\n'
printf '  Authorized origin:       https://%s\n' "$FQDN"
printf '  Authorized redirect URI: https://%s/api/v1/auth/oidc/callback\n' "$FQDN"
if is_interactive && [[ -z "$GOOGLE_CLIENT_ID" ]] && [[ "$(uname -s)" == "Darwin" ]]; then
  open 'https://console.cloud.google.com/auth/clients' >/dev/null 2>&1 || true
fi
prompt GOOGLE_CLIENT_ID "Google OAuth client ID"
prompt_secret GOOGLE_CLIENT_SECRET "Google OAuth client secret"
[[ "$GOOGLE_CLIENT_ID" == *.apps.googleusercontent.com ]] || die "Google client ID should end in .apps.googleusercontent.com"

write_tfvars

say "Configuration complete"
printf 'AWS account: %s\n' "$(aws sts get-caller-identity --query Account --output text)"
printf 'Public URL:  https://%s\n' "$FQDN"
printf 'DNS:         Cloudflare %s -> Route 53 %s\n' "$ROOT_DOMAIN" "$ROUTE53_ZONE_ID"
printf 'Schedule:    08:00-22:00 %s\n' "$OFF_HOURS_TIMEZONE"
if [[ "${SOULACY_QUICKSTART_CONFIGURE_ONLY:-}" == "1" ]]; then
  printf 'Configuration-only mode requested; AWS application resources were not deployed.\n'
  exit 0
fi
printf 'The deployment will now build images and create billable AWS resources.\n'
if is_interactive; then
  confirm "Deploy Soulacy Team Lite now?" || { printf 'Configuration saved. Run deploy/aws/deploy.sh --mode team-lite when ready.\n'; exit 0; }
fi

export SOULACY_AWS_REGION="$AWS_REGION"
export SOULACY_AWS_BUDGET_EMAIL="$BUDGET_EMAIL"
export SOULACY_AWS_OFF_HOURS_TIMEZONE="$OFF_HOURS_TIMEZONE"
export SOULACY_AWS_OIDC_CLIENT_SECRET="$GOOGLE_CLIENT_SECRET"
exec "$SCRIPT_DIR/deploy.sh" --mode team-lite --var-file "$VAR_FILE"
