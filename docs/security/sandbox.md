# Tool isolation

Soulacy uses two different protections. They are intentionally named here so
operators do not mistake resource limits for a security boundary.

## Privileged builtins: disposable container boundary

`shell_exec`, `run_script`, `python_eval`, `install_library`, `write_file`, and
`download_file` all pass through one privileged-tool chokepoint. With the
shipped configuration, command execution uses a new Docker container per call:

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
    image: python:3.12-slim
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

## Ordinary Python tools: resource limits by default

Agent/plugin Python execution still uses the `__exec-sandbox` POSIX rlimit
wrapper by default. It caps CPU, address space, open descriptors, and
single-file size and filters the environment. This wrapper is a
resource-exhaustion guard, not a filesystem or network boundary.

For untrusted agent tools, select the Docker backend per agent:

```yaml
# SOUL.yaml
execution:
  backend: docker
```

Each tool call then runs in a disposable container with the same hardened
profile as privileged builtins: read-only root, all capabilities dropped,
`no-new-privileges`, no network by default, a `noexec` tmpfs `/tmp` (also
`HOME`), and the `runtime.sandbox` CPU, memory, PID, file-descriptor and
file-size caps. No gateway environment is forwarded, and only the volumes you
list are mounted. If Docker is unreachable, the call fails; it never falls back
to the host.

```yaml
# config.yaml
executor:
  docker_image: python:3.12-slim
  docker_network: none        # an explicit choice; "none" also blocks cloud metadata
  docker_user: auto           # auto (gateway uid:gid) | image | "uid:gid"
  docker_volumes:
    - /srv/agent-data:/data:rw
```

`docker_user: auto` runs the tool as the gateway's own user, so it is not
root inside the container and files it writes to a volume belong to you. Use
`image` to keep the image's default user.

Current limits: tools defined with `python_file` load from a host path, so
they only work in Docker if that directory is mounted at the same path. Plugin
tools don't honour `execution.backend` yet. Both are planned; see
[the design memo](../CONTAINER_SANDBOX_DESIGN.md).

On macOS `RLIMIT_AS` is advisory; on non-Unix systems the rlimit wrapper is a
no-op. These limitations do not weaken the Docker boundary for privileged
builtins.
