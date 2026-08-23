# Disaster recovery

## Recovery order

1. Restore PostgreSQL and verify point-in-time consistency before accepting writes.
2. Restore the external KMS key policy/alias and validate unwrap using a non-production probe. KMS failure must keep the gateway closed.
3. Restore NATS JetStream and shared artifacts. Do not replay execution jobs until run records and idempotency records are available.
4. Deploy gateways read-only, verify tenancy and entitlement state, then deploy signed execution workers with network disabled.
5. Reconcile queued/running records: retry only runs with no recorded external side effect; mark the others for operator review.
6. Enable writes for a canary workspace, validate OIDC, one read, one run, cancellation, audit, billing webhook, backup and restore, then expand.

## Required drills

- Quarterly PostgreSQL point-in-time restore into an isolated account.
- Quarterly KMS loss/permission-denied exercise proving fail-closed startup.
- Monthly NATS worker-loss exercise proving gateway availability and redelivery.
- Quarterly signed-image rollback and egress-proxy denial test.

Record recovery-point and recovery-time results. A backup is not considered valid until a clean environment has restored and passed the canary sequence.
