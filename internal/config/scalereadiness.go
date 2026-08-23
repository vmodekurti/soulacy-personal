package config

// scalereadiness.go — what `scale` mode does not yet deliver.
//
// WHY THIS EXISTS. Scale mode's premise is that you can run more than one copy
// of the gateway. Its startup validation enforces the INFRASTRUCTURE for that —
// a distributed queue, a shared artifact store — and enforces nothing about
// whether the program itself is ready for it. It is not, yet, in specific and
// enumerable ways.
//
// That gap was invisible in the worst way: an operator configures everything
// scale mode asks for, every check passes, the process starts, and the
// remaining hazards only appear as intermittent wrong behaviour under a second
// replica. Passing a checklist reads as an assurance.
//
// The list is CODE rather than documentation so it is reported by the running
// system to the person actually running it, and so removing an item requires
// deleting a line here — which is a decision somebody makes, not a doc that
// drifts.

// ScaleReplicationBlockers returns the reasons running more than one gateway
// replica is not yet safe, in the order an operator would hit them.
//
// Empty means multi-replica operation is believed sound. Anything else should
// be shown at startup and by the health checks, because the failures it
// describes are silent: none of them produce an error, they produce wrong
// answers.
func ScaleReplicationBlockers() []string {
	return nil
}

// resolvedScaleBlockers is the list this file used to carry and no longer
// does, kept as a comment rather than deleted so the next person to read
// ScaleReplicationBlockers can see what MOVED off it rather than wondering
// whether it was ever there:
//
//   - "a retried request that lands on another replica is executed a second
//     time" — the idempotency replay cache is durable now
//     (gateway.OpenIdempotencyCache), so a retry replays wherever it lands.
//   - "the scheduler's repeated-failure counters are per-process" — the count
//     lives on the schedule row (schedules.RecordFailure), so failures
//     accumulate across replicas and the auto-disable limit is reachable.
//   - "a conversation in progress lives in one replica's memory" — Team/Scale
//     now use shared PostgreSQL history, hydrate a new replica before the next
//     turn, key the local cache by workspace, and serialize concurrent turns
//     with a PostgreSQL advisory lock.
//   - "artifacts are stored on each replica's own disk" — completed workboard
//     and chat artifacts are copied to the configured shared S3/POSIX object
//     store, metadata carries opaque object references, downloads read through
//     that store, and workspace erasure deletes the tenant object prefix.
//   - "a run paused for approval can only be released by the replica it paused
//     on" — Team/Scale approvals now use PostgreSQL and blocked engines observe
//     the shared decision as well as their local rendezvous channel.
//   - "cancelling a chat or stream run only works on its executing replica" —
//     active chat controls and cancellation requests now use a workspace-scoped
//     PostgreSQL mailbox polled by the context-owning replica.
//   - "a reconnecting client cannot resume on another replica" — events fan
//     out through the deployment queue for live delivery and PostgreSQL action
//     event IDs provide shared, workspace-bound replay cursors.
//
// ScaleIsReplicationReady reports whether the blocker list is empty.
func ScaleIsReplicationReady() bool { return len(ScaleReplicationBlockers()) == 0 }
