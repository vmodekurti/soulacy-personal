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

## Ordinary Python tools: resource limits only

Agent/plugin Python execution still uses the `__exec-sandbox` POSIX rlimit
wrapper. It caps CPU, address space, open descriptors, and single-file size and
filters the environment. This wrapper is a resource-exhaustion guard, not a
filesystem or network boundary. Use a Docker executor for untrusted ordinary
Python tools as well.

On macOS `RLIMIT_AS` is advisory; on non-Unix systems the rlimit wrapper is a
no-op. These limitations do not weaken the Docker boundary for privileged
builtins.
