# Disaster recovery and release assurance

Soulacy Team and Scale releases are gated by a production-like restore drill.
The release workflow will not publish the container or GitHub release unless
PostgreSQL restore, object integrity, encrypted-secret recovery, vector tenant
filtering, schema consistency, load targets, race/chaos tests, and the current
independent security review all pass.

## Recovery objectives

| Subsystem | RPO | RTO | Required protection |
|---|---:|---:|---|
| PostgreSQL | 5 minutes | 60 minutes | Continuous WAL archive, daily base backup, point-in-time recovery |
| Shared object storage | 15 minutes | 2 hours | Versioning plus cross-region replication or immutable snapshots |
| Credential vault | 5 minutes | 60 minutes | Encrypted snapshots and tested access to the external KMS |
| Vector index | 5 minutes (source records) | 4 hours | Snapshot restore or deterministic rebuild from relational references |

The machine-readable source for these commitments is
`internal/recovery/policy.go`; tests fail if a required subsystem lacks a
positive objective or recovery method.

## Required backup configuration

PostgreSQL must archive WAL continuously and take a base backup at least daily.
Monitor the age of the last successfully archived WAL segment; alert before it
reaches five minutes. Retain at least two known-good base-backup generations in
separate failure domains.

The artifact bucket must enable object versioning and either cross-region
replication or immutable snapshots. Deny lifecycle changes to the application
role. Credential-vault backups must remain encrypted and the restore role must
be able to unwrap live workspace DEKs through the configured AWS KMS or Vault
Transit service.

Workspace deletion removes live wrapped DEKs and ciphertext. Older immutable
backup generations may retain pre-deletion ciphertext until their documented
retention expires; they remain access-controlled, must never selectively
resurrect a deleted workspace, and a full restore must reapply deletion records
created after the selected recovery point before the gateway accepts traffic.

## Recovery order

1. Stop ingress and keep gateway readiness false.
2. Restore PostgreSQL to the selected point in time.
3. Apply every committed schema migration and compare the schema report with
   the release version.
4. Restore the shared object bucket and verify stored checksums for every
   relational artifact reference.
5. Restore the external KMS key policy/alias and encrypted credential vault;
   validate unwrap using a non-production canary secret without printing it.
6. Restore NATS JetStream. Do not replay jobs until run and idempotency records
   are available.
7. Restore the vector snapshot, or rebuild from relational source records.
   Verify every reference and run the two-workspace live pre-filter test.
8. Reapply workspace deletion/tombstone records newer than the recovery point.
9. Reconcile queued/running records: retry only runs with no recorded external
   side effect; route all others to operator review.
10. Run `make production-assurance`, inspect API and queue p50/p95/p99 plus
    error rates, then enable a canary workspace before general ingress.

## Release evidence

The workflow uploads `production-assurance-<commit>` with restore, live-Qdrant,
load, isolation, race, and chaos logs. It requires the repository secret
`SOULACY_SECURITY_REVIEW_ATTESTATION` in this form:

```json
{
  "reviewer": "Independent Security Firm",
  "report_url": "https://security.example/reports/soulacy-2026-08",
  "completed_at": "2026-08-23T12:00:00Z",
  "critical_open": 0,
  "high_open": 0,
  "reviewed_commit": "full-git-commit-sha"
}
```

The attestation must be no older than 90 days, cover the exact release commit,
link to an HTTPS report, and contain zero unresolved critical or high findings.
A missing or invalid attestation blocks publication.

## Required drills

- Quarterly PostgreSQL point-in-time restore into an isolated account.
- Quarterly KMS loss/permission-denied exercise proving fail-closed startup.
- Monthly NATS worker-loss exercise proving gateway availability and redelivery.
- Quarterly signed-image rollback and egress-proxy denial test.

Record actual recovery-point and recovery-time results. A backup is not valid
until a clean environment has restored and passed the canary sequence.
