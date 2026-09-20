#!/usr/bin/env bash
# Upgrade a one-click Azure Soulacy VM from your laptop, without SSH, via Azure
# Run Command. The upgrade runs ON the VM, so it is not replaced and its data
# stays intact.
#
#   deploy/azure/upgrade-remote.sh <resource-group> <vm-name>
set -euo pipefail
RG="${1:?usage: $0 <resource-group> <vm-name>}"
VM="${2:?usage: $0 <resource-group> <vm-name>}"
az vm run-command invoke -g "$RG" -n "$VM" --command-id RunShellScript \
  --scripts 'export SOULACY_UPGRADE_DIR=/opt/soulacy; curl -fsSL https://raw.githubusercontent.com/vmodekurti/soulacy-personal/main/upgrade.sh | bash' \
  --query 'value[0].message' -o tsv | tail -25
