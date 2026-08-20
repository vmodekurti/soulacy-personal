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
	return []string{
		"artifacts are stored on each replica's own disk, so a file written by one replica is not " +
			"readable by another (deployment.shared_artifact_store is recorded but not yet used)",
		"a conversation in progress lives in one replica's memory, so a follow-up message routed " +
			"elsewhere starts from nothing",
		"a run paused for approval can only be released by the replica it paused on",
		"cancelling a CHAT OR STREAM run only works if the request reaches the replica executing " +
			"it; durable runs are cancelled through their record and do reach any replica",
		"a client reconnecting to a different replica cannot resume the event stream where it left off",
	}
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
//
// The cancellation entry was also NARROWED rather than removed: durable runs
// have always been cancelled through their record, which any replica can
// write and the executing one polls. Only chat and stream runs, which live in
// an in-memory registry, are replica-local. An overstated hazard costs the
// list its credibility as surely as a missing one.

// ScaleIsReplicationReady reports whether the blocker list is empty.
func ScaleIsReplicationReady() bool { return len(ScaleReplicationBlockers()) == 0 }
