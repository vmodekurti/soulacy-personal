# Adversarial execution isolation tests

The release gate must exercise a worker image under the production runtime and verify:

- host paths (`/etc`, `/proc/1/root`, Docker socket, gateway config, databases) are absent;
- sibling workspace paths and symlink escapes are unreadable;
- cloud metadata, RFC1918, loopback, DNS rebinding, raw sockets and direct internet egress fail;
- the only enabled egress path is the authenticated policy proxy, with an allowlisted hostname;
- capabilities, setuid, ptrace, mount, namespace creation and device access fail;
- PID, memory, CPU, file-size, output-size and wall-clock limits terminate the job;
- cancellation kills the container and worker loss redelivers an unacknowledged job;
- an unsigned, tag-only, or incorrectly signed image prevents worker readiness;
- duplicate Stripe events are idempotent and payment failure blocks writes/runs while reads and remediation remain available;
- KMS denial or workload-identity loss prevents gateway startup and emits metadata-only audit events.

Run unit gates with `make security`. Run the production image smoke on a
disposable gVisor worker node (never a shared worker node):

```bash
SOULACY_EXECUTION_IMAGE='registry.example/execution@sha256:...' \
SOULACY_COSIGN_KEY=/etc/soulacy/execution-image.pub \
bash scripts/execution-sandbox-smoke.sh
```

The smoke verifies image trust, runtime admission, host/sibling path absence,
Docker socket absence, read-only root, zero capabilities, no-new-privileges,
and denial of metadata, loopback, private, and public direct network routes.
Egress-proxy allow/deny behavior must be tested separately against the actual
production proxy policy because the default execution network is deliberately
`none`.
