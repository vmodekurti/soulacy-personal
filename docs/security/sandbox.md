# Tool isolation

Soulacy uses two different protections. They are intentionally named here so
operators do not mistake resource limits for a security boundary.

## Local execution: disposable container boundary

Ordinary agent Python plus `shell_exec`, `run_script`, `python_eval`,
`install_library`, `write_file`, and `download_file` pass through the isolated
execution chokepoints. With the shipped configuration, command execution uses
a new Docker container per call:

- no network (`--network none`), including no cloud metadata endpoint;
- read-only container root, all Linux capabilities dropped, and
  `no-new-privileges`;
- PID, memory, CPU, file-descriptor, file-size, and caller wall-clock bounds;
- only `<workspace>/data/sandbox` mounted at `/workspace`;
- no gateway environment, config file, credential vault, database, logs, agent
  manifests, or other host directories;
- failure to contact Docker refuses the call; there is no host fallback.

Scripts must therefore be written into the isolated workspace before
`run_script` can execute them. Package installation is ephemeral and, with the
default network-off policy, cannot download from public registries.

```yaml
runtime:
  sandbox:
    enabled: true
    mode: docker
    image: registry.example/soulacy-execution@sha256:<digest>
    container_runtime: runsc
    require_signed_image: true
    cosign_key: /etc/soulacy/execution-image.pub
    cpu_seconds: 30
    memory_mb: 512
    open_files: 256
    file_size_mb: 64
    pids: 128
```

`mode: unsandboxed` (or legacy `enabled: false`) is an explicit compatibility
escape hatch. It runs commands as the gateway user and emits an error-level
warning on every startup. Do not use it on shared or production systems.

Filesystem-native `write_file` and `download_file` still execute in the host
process after the chokepoint, but their targets pass the same symlink-aware
workspace containment policy used by all filesystem tools. They cannot write
outside configured roots.

## Ordinary Python tools: remote execution plane

Team and Scale deployments set `executor.backend: worker`. The gateway publishes
jobs to NATS JetStream and never starts tenant Python. Stateless
`soulacy-worker` processes consume those jobs and run the digest-pinned,
Cosign-verified image under the configured hardened OCI runtime (`runsc` for
gVisor). The worker has no HTTP listener and never loads the gateway config;
its narrow `SOULACY_WORKER_*` environment contains only NATS identity,
execution-image trust, limits, and workspace-mount settings.

Readiness is end-to-end: Team and Scale require a response from a live worker,
not merely a successful connection to NATS. The execution boundary is a
non-waivable startup requirement even when an operator has acknowledged other
temporarily unavailable multi-user infrastructure.

Network is `none` by default. An egress-enabled sandbox must name a dedicated
egress network and an authenticated proxy; the proxy is responsible for
enforcing `allowed_egress_hosts`, DNS policy, byte limits, and audit records.
Direct bridge networking without a proxy is rejected in Team and Scale.

Never mount a host container-runtime socket into the gateway. A runtime socket
is an administrative API, not a sandbox. Workers may access the runtime only on
dedicated execution nodes; the disposable workload containers they create
never receive the socket.

Personal mode retains the POSIX process wrapper only behind the explicit
`runtime.sandbox.mode: unsandboxed` compatibility escape hatch. It is not a
tenant security boundary and is never permitted in Team or Scale.

On macOS `RLIMIT_AS` is advisory; on non-Unix systems the rlimit wrapper is a
no-op. These limitations do not weaken the Docker boundary for privileged
builtins.
