# Execution security incident response

1. Disable the affected organization/workspaces and stop the execution-worker deployment. Keep gateways read-only so owners can export evidence.
2. Revoke the worker workload identity, KMS grants, NATS credentials, egress-proxy identity, registry token, and any workspace credentials used by affected runs.
3. Preserve immutable copies of deployment audit events, NATS stream metadata, worker/container logs, image digest and signature bundle, run records, and egress-proxy decisions. Never place decrypted secrets in the evidence bundle.
4. Quarantine the execution image and worker node pool. Rebuild nodes from a known-good image; do not return compromised nodes to service.
5. Determine affected workspace/run IDs from the durable job and audit identifiers. Notify only after scope is evidence-backed.
6. Patch and run the isolation escape suite, sign a new digest-pinned image, rotate workspace data keys and external KMS grants, then restore a canary workspace before broader reactivation.
7. Record timeline, detection gap, containment time, affected data, customer communication, and preventive actions in the post-incident review.

The deployment administrator owns platform response. Workspace owners can see their workspace run/audit records but cannot restart workers, read platform logs, or access another tenant's evidence.
