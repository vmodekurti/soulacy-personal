#!/usr/bin/env bash
set -Eeuo pipefail

AWS_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
TMP_DIR=$(mktemp -d "${TMPDIR:-/tmp}/soulacy-quickstart-test.XXXXXX")
cleanup() { rm -rf -- "$TMP_DIR"; }
trap cleanup EXIT
mkdir -p "$TMP_DIR/bin"

cat >"$TMP_DIR/bin/aws" <<'MOCK'
#!/usr/bin/env bash
case "$1 $2" in
  "configure list-profiles") printf 'default\n' ;;
  "sts get-caller-identity") printf '123456789012\n' ;;
  "route53 list-hosted-zones-by-name") printf '{"HostedZones":[]}' ;;
  "route53 create-hosted-zone") printf '{"HostedZone":{"Id":"/hostedzone/ZSOULACYTEST"}}' ;;
  "route53 get-hosted-zone") printf '{"DelegationSet":{"NameServers":["ns-1.awsdns.test","ns-2.awsdns.test","ns-3.awsdns.test","ns-4.awsdns.test"]}}' ;;
  *) printf 'unexpected aws call: %s\n' "$*" >&2; exit 1 ;;
esac
MOCK

cat >"$TMP_DIR/bin/curl" <<'MOCK'
#!/usr/bin/env bash
while IFS= read -r _; do :; done
url="${!#}"
case "$url" in
  *'/zones?name=soulac.io'*) printf '{"success":true,"result":[{"id":"cf-zone","name":"soulac.io"}]}' ;;
  *'/dns_records?name=team.soulac.io'*) printf '{"success":true,"result":[]}' ;;
  *'/dns_records') printf '{"success":true,"result":{"id":"record"}}' ;;
  *) printf '{"success":false,"errors":[{"code":1,"message":"unexpected URL"}]}' ;;
esac
MOCK

for command in docker terraform cosign; do
  cat >"$TMP_DIR/bin/$command" <<'MOCK'
#!/usr/bin/env bash
exit 0
MOCK
done
chmod 0755 "$TMP_DIR/bin/"*

PATH="$TMP_DIR/bin:$PATH" \
AWS_PROFILE=default \
SOULACY_AWS_BUDGET_EMAIL=owner@example.com \
SOULACY_ROOT_DOMAIN=soulac.io \
SOULACY_SUBDOMAIN=team \
CLOUDFLARE_API_TOKEN=test-token \
GOOGLE_OIDC_CLIENT_ID=test.apps.googleusercontent.com \
GOOGLE_OIDC_CLIENT_SECRET=test-secret \
SOULACY_AWS_OFF_HOURS_TIMEZONE=America/Chicago \
SOULACY_AWS_VAR_FILE="$TMP_DIR/terraform.tfvars" \
SOULACY_QUICKSTART_CONFIGURE_ONLY=1 \
  "$AWS_DIR/team-lite-quickstart.sh" >"$TMP_DIR/output"

grep -q 'domain_name = "team.soulac.io"' "$TMP_DIR/terraform.tfvars"
grep -q 'route53_zone_id = "ZSOULACYTEST"' "$TMP_DIR/terraform.tfvars"
grep -q 'oidc_issuer = "https://accounts.google.com"' "$TMP_DIR/terraform.tfvars"
grep -q 'oidc_client_id = "test.apps.googleusercontent.com"' "$TMP_DIR/terraform.tfvars"
if grep -q 'test-secret\|test-token' "$TMP_DIR/terraform.tfvars"; then
  echo 'a secret leaked into terraform.tfvars' >&2
  exit 1
fi
grep -q 'Configuration-only mode requested' "$TMP_DIR/output"
printf 'PASS: Team Lite quickstart DNS and configuration flow\n'
