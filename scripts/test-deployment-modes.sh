#!/usr/bin/env bash
set -Eeuo pipefail

repo_dir=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$repo_dir"

printf '==> Personal / Team / Scale configuration contracts\n'
go test ./internal/config -run 'TestDeploymentMode|TestValidate_(Team|Scale|Unsafe)' -count=1

printf '==> Execution-worker dispatch and end-to-end readiness\n'
go test ./internal/executor/remote ./internal/app ./internal/gateway \
  -run 'Test(GatewayDispatchesExecutionToWorker|Probe|Readiness)' -count=1

printf '==> Worker secret-boundary configuration\n'
go test ./cmd/soulacy-worker -count=1

printf '==> AWS execution-plane static contracts\n'
rg -q 'SOULACY_EXECUTION_ROOT=/var/lib/soulacy' deploy/aws/templates/worker-user-data.sh.tftpl
rg -q -F '${efs_id}:/ /var/lib/soulacy' deploy/aws/templates/worker-user-data.sh.tftpl
rg -q 'aws_security_group.worker' deploy/aws/security.tf
rg -q 'elasticfilesystem:ClientMount' deploy/aws/iam.tf
rg -q -F 'subscribe: ["soulacy.execution.jobs", "_INBOX.>"]' deploy/aws/templates/nats-user-data.sh.tftpl
if rg -n 'docker.sock|--privileged' deploy/aws/templates/gateway-user-data.sh.tftpl deploy/aws/templates/worker-user-data.sh.tftpl \
  || rg -n -- '--network[ =]host' deploy/aws/templates/worker-user-data.sh.tftpl \
  || (rg -n -- '--network[ =]host' deploy/aws/templates/gateway-user-data.sh.tftpl | rg -v 'soulacy-cloudflared'); then
  printf 'AWS gateway/worker templates contain a prohibited runtime escape\n' >&2
  exit 1
fi

if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
  printf '==> Personal Compose contracts\n'
  SOULACY_API_KEY=test-only-key POSTGRES_PASSWORD=test-only-password \
    docker compose -f docker-compose.lite.yml config --quiet
  SOULACY_API_KEY=test-only-key POSTGRES_PASSWORD=test-only-password \
    docker compose -f docker-compose.yml config --quiet
  if rg -n '/var/run/docker.sock|docker.sock:' docker-compose.yml docker-compose.lite.yml; then
    printf 'gateway Compose must not mount a container-runtime socket\n' >&2
    exit 1
  fi
else
  printf 'SKIP: Docker Compose CLI is unavailable; Go deployment contracts still passed.\n'
fi

if command -v terraform >/dev/null 2>&1; then
  printf '==> AWS Terraform formatting, validation, and mode plans\n'
  # Never reuse an operator's initialized backend or credentials. Contract
  # tests are read-only and must behave identically on a laptop and in CI.
  terraform_data_dir="$(mktemp -d "${TMPDIR:-/tmp}/soulacy-terraform-test-XXXXXXXX")"
  trap 'rm -rf "$terraform_data_dir"' EXIT
  export TF_DATA_DIR="$terraform_data_dir"
  terraform -chdir=deploy/aws fmt -check -recursive
  terraform -chdir=deploy/aws init -backend=false -input=false -reconfigure
  terraform -chdir=deploy/aws validate
  terraform -chdir=deploy/aws test
else
  printf 'SKIP: Terraform is unavailable; AWS profile tests were not executed.\n'
fi

printf 'PASS: deployment-mode contract suite completed.\n'
