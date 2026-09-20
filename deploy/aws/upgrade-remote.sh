#!/usr/bin/env bash
# Upgrade a one-click AWS Soulacy VM from your laptop, without SSH, via AWS
# Systems Manager (the one-click template enables it). The upgrade runs ON the
# instance, so it is not replaced and its data volume stays intact.
#
#   deploy/aws/upgrade-remote.sh <instance-id> [--profile <p>] [--region <r>]
set -euo pipefail
ID="${1:?usage: $0 <instance-id> [--profile p] [--region r]}"; shift || true
CMD='export SOULACY_UPGRADE_DIR=/opt/soulacy; curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/upgrade.sh | bash'
CID=$(aws ssm send-command "$@" --instance-ids "$ID" --document-name AWS-RunShellScript \
  --comment "soulacy upgrade" --parameters "commands=[\"$CMD\"]" \
  --query 'Command.CommandId' --output text)
echo "sent SSM command $CID — waiting for the instance to finish…"
ST=Pending
for _ in $(seq 1 90); do
  ST=$(aws ssm get-command-invocation "$@" --command-id "$CID" --instance-id "$ID" --query Status --output text 2>/dev/null || echo Pending)
  case "$ST" in Success|Failed|Cancelled|TimedOut) break ;; esac
  sleep 5
done
echo "status: $ST"
aws ssm get-command-invocation "$@" --command-id "$CID" --instance-id "$ID" --query StandardOutputContent --output text | tail -25
[ "$ST" = "Success" ]
