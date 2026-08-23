# Framework security review remediation

This page maps the external framework review to the production code path. A
finding is closed only when the enforcement is in code and covered by an
automated gate; documentation or an operator recommendation alone is not a
remediation.

| Domain | Disposition | Enforced by | Verification |
| --- | --- | --- | --- |
| Agent memory | Closed | `internal/agentmemory/store.go` reads JSONL backwards from EOF in bounded blocks and rotates at 8 MiB. | `TestEpisodic_ReadRecentReadsBackwardFromEOF` proves a two-record read of a 4 MiB log consumes at most one 64 KiB block. |
| Rate limiting | Closed | `internal/ratelimit/counter.go` uses an atomic Redis Lua sorted-set sliding window, Redis server time, cluster hash tags, and no fallback when Redis mode is selected. | `TestLiveRedisSlidingWindow` runs against real Redis in the release gate. Scale configuration requires Redis. |
| Channel ingress | Closed for Team/Scale | `internal/channels/channel.go` places adapter ingress behind the configured durable queue and acknowledges only after processing. | Durable-ingress tests cover redelivery and idempotent handling. |
| Storage IPC context | Closed | `internal/extstorage/storage.go` derives sidecar deadlines from the caller context. Runtime memory reads use the context-aware workspace interfaces. | Cancellation tests prove a caller deadline terminates an in-flight sidecar call. |
| Tool sandbox | Closed for Team/Scale | Team/Scale startup requires `executor.backend: worker`, a hardened OCI runtime, a digest-pinned signed image, and Docker isolation for privileged tools. Runtime refuses local execution when isolated execution is required. `internal/sandbox` is a Personal-mode resource guard, not the SaaS boundary, and now fails closed if limits cannot be installed. | `TestMultiUserPythonAlwaysUsesIsolatedExecutor`, `TestMultiUserPythonFailsClosedWithoutWorker`, container escape tests, and deployment prerequisite tests run in `make security`. |
| Vector tenancy | Closed | The base `sdk/vector.Backend.Search` contract now requires `workspaceID`; all built-in backends reject an empty workspace. External sidecars must advertise `vector.workspace` before tenant traffic is sent. SQLite and Qdrant pre-filter by workspace before KNN selection. | Unit isolation tests plus `TestLiveQdrantEnforcesWorkspacePrefilter` against real Qdrant in the release gate. |
| Tenant audit query | Closed | `tenant_mutation_audit.workspace_id` is populated transactionally, existing rows are backfilled, and `(workspace_id, created_at DESC, id DESC)` serves tenant keyset reads. Actor, action, and resource indexes remain for their respective query paths. | PostgreSQL schema and tenant-audit tests run in CI and the live recovery gate. |

The local Personal mode intentionally keeps low-dependency compatibility
paths, including in-process counters and the POSIX resource-limit wrapper.
Those paths are not silently reachable from Team or Scale: production-mode
validation and runtime dispatch both fail closed. This distinction prevents a
single-user portability feature from being represented as a multi-tenant
security boundary.

Publication remains gated on live PostgreSQL, Redis, and Qdrant checks, load
and race suites, recovery evidence, and an independent-review attestation for
the exact release commit.
