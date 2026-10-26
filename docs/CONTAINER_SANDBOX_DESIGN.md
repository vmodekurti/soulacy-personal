# Container Sandbox for Tool Code: Design Memo

**Status:** proposed. Phase 1 ships in the PR that adds this memo.
**Date:** 2026-10-25

## 1. Problem

Soulacy runs tool code on three paths, and only one of them has a security
boundary:

| Path | Code | Isolation today |
|---|---|---|
| Privileged builtins (`shell_exec`, `run_script`, `python_eval`, `install_library`, …) | `runtime.DockerPrivilegedRunner` | Disposable container per call: `--network none`, `--read-only`, `--cap-drop ALL`, `no-new-privileges`, PID/memory/CPU/ulimit caps, only `<workspace>/data/sandbox` mounted. Docker unavailable means the call is refused (no host fallback). |
| Agent Python tools (`python_file` / `inline`) | `runtime.runPythonToolOnce` | **Resource limits only.** The `__exec-sandbox` wrapper applies setrlimit and filters the environment. The tool runs as the gateway user with full filesystem and network access. Agents can opt into `execution.backend: docker`. |
| Plugin Python tools (`plugin__<id>__<tool>`) | `runtime` plugin dispatch | **Resource limits only**, with no container option at all. |

The opt-in `execution.backend: docker` path (`internal/executor/docker`)
is much weaker than the privileged runner. It runs
`docker run --rm -i --network <cfg> <image> python3 -c …` with a writable
root filesystem, the image's default user (root), every default capability,
and no PID, memory or CPU limits. So "run untrusted tools in Docker", the
advice in [Tool isolation](security/sandbox.md), currently buys network
isolation and a separate filesystem, but not a hardened boundary.

There are also two usability gaps:

- **`python_file` tools fail in Docker.** The bootstrap script loads the
  tool by its *host* path, which doesn't exist in the container unless the
  operator mounts it at the same path via `executor.docker_volumes`.
- **The two container paths have drifted.** Each builds its own
  `docker run` argv, so a hardening fix made in one doesn't reach the other.

## 2. Goals and non-goals

**Goals**

1. One hardened container profile, defined once, used by every container
   execution path.
2. Untrusted agent and plugin Python tools can run behind that boundary,
   with the same fail-closed rule as privileged builtins.
3. Existing agents keep working. Changing a default must be deliberate,
   phased and documented.
4. Work on every host where Soulacy runs today: Linux, macOS (Docker
   Desktop or colima) and Windows (Docker Desktop).

**Non-goals (for now)**

- Building a container runtime, or depending on a Docker SDK. Soulacy
  stays a single static binary that shells out to the `docker` CLI.
- Firecracker/microVM orchestration inside Soulacy. Stronger runtimes plug
  in through Docker's `--runtime` (gVisor `runsc`, Kata) instead. See §6.
- Multi-tenant isolation. That is the multi-user edition's concern; this
  memo covers one operator running agents they partly trust.

## 3. Design

### 3.1 Shared profile: `sandbox.ContainerProfile`

A new type in `internal/sandbox` owns the hardened `docker run` argv:

```go
type ContainerProfile struct {
    Network string   // default "none"
    Limits  Limits   // memory, CPU, open files, file size
    PIDs    int      // default 128
    TmpfsMB int      // default 64; /tmp is the only writable path besides mounts
    User    string   // "" = image default; "uid:gid" otherwise
    Workdir string
    Mounts  []string // -v specs; nothing is mounted implicitly
    Env     []string // KEY=VALUE, already filtered by the caller
}

func (p ContainerProfile) RunArgs(image string, argv ...string) []string
```

It always emits `--rm -i --read-only --cap-drop ALL --security-opt
no-new-privileges --pids-limit N --tmpfs /tmp:rw,noexec,nosuid,size=…`.
Resource flags are emitted only when the matching limit is set, as today.
`DockerPrivilegedRunner` and `executor/docker` both build their command
lines through it.

### 3.2 Hardened Docker executor (`execution.backend: docker`)

`executor/docker` adopts the profile, plus defaults that keep Python tools
working on a read-only root:

- working directory and `HOME` are `/tmp`, and `PYTHONDONTWRITEBYTECODE=1`
  (no `.pyc` writes to a read-only image);
- limits come from `runtime.sandbox` (`cpu_seconds`, `memory_mb`,
  `open_files`, `file_size_mb`, `pids`), the same caps privileged builtins
  already get;
- the gateway environment is still never forwarded;
- user: `executor.docker_user`, default `auto`.

**Container user.** `auto` runs as the gateway's own uid:gid. Files the tool
writes to an allowlisted volume are then owned by the operator, not root.
The process is also unprivileged, and that matters with `--cap-drop ALL`:
root without `CAP_DAC_OVERRIDE` can't write to a host directory owned by
the operator on Linux anyway. `image` keeps the image's default user (the
old behaviour), and an explicit `uid:gid` is passed through. On Windows,
`auto` behaves like `image`.

### 3.3 Fail-closed

Each path that adopts the container boundary refuses the call when Docker
is unreachable, and never silently falls back to the host. The privileged
runner already works this way; the Docker executor returns an error
without falling back. When later phases make containers the default for
ordinary tools (§4), they must keep this rule and report it clearly at
startup (as `soulacy doctor` / `sy launch check` already do for privileged
tools).

### 3.4 Tool code in the container (phase 2)

To make `python_file` tools work, the engine will mount the tool's
directory read-only at a fixed container path (`/tool`) and rewrite the
bootstrap to load from there. This needs an executor interface change,
because the backend has to know which host directory to mount per call.
Plugin tools get the same treatment from the plugin's directory.

## 4. Rollout

| Phase | Change | Default impact |
|---|---|---|
| **1 (this PR)** | `sandbox.ContainerProfile`; the privileged runner refactored onto it with identical argv; `executor/docker` hardened and limited from `runtime.sandbox`; `executor.docker_user` (default `auto`); docs; a real-Docker integration test (`SOULACY_DOCKER_TESTS=1`). | Only agents that opted into `execution.backend: docker`. Their containers become read-only, capability-free and non-root. |
| 2 | Automatic read-only mount of tool and plugin directories; plugin tools honour `execution.backend`; pull the sandbox image at startup (and report it in `soulacy doctor`), so the first tool call doesn't spend its timeout downloading the image. | `python_file` and plugin tools work in Docker. |
| 3 | `runtime.sandbox.python_tools: container \| limits` (default `limits`); per-agent override; `sy launch check` reports which agents run unisolated. | Opt-in. |
| 4 | Flip the default to `container` when Docker is available, after one release of warnings; a warm container pool to recover latency (one container per agent session, `docker exec` per call). | Default change, announced in release notes. |
| 5 | Pluggable runtimes: `runtime.sandbox.runtime: runsc` (gVisor) / `kata`; Podman as a drop-in CLI (`runtime.sandbox.binary`); optional egress allowlist through a filtering proxy instead of all-or-nothing networking. | Opt-in. |

## 5. Security notes

- **Root filesystem.** With `--read-only` and a `noexec` `/tmp`, a tool
  can't persist anything or run dropped binaries outside allowlisted
  volumes.
- **Capabilities.** `--cap-drop ALL` with `no-new-privileges` blocks setuid
  escalation and raw sockets, even if the network were enabled.
- **Network.** The default `none` also cuts off the cloud metadata
  endpoint. `executor.docker_network` remains the explicit operator choice.
- **Environment.** Gateway secrets never enter the container, because the
  executor passes no host environment. An agent's `env:` allowlist will be
  forwarded in phase 2, matching the subprocess path.
- **Residual risk.** Containers share the host kernel. A kernel exploit
  escapes any Docker profile. Operators who need protection from that
  should use gVisor or Kata (phase 5) or run Soulacy inside a VM.
- **Docker socket.** The gateway calling `docker` means anyone who controls
  the gateway process controls Docker. That is already true for privileged
  builtins and doesn't change.

## 6. Alternatives considered

- **Native namespaces (bubblewrap / nsjail / Landlock).** Lighter than
  Docker, but Linux-only, and macOS (where most personal installs run) has
  no equivalent besides the deprecated `sandbox-exec`. It could be added
  later as a backend behind the same fail-closed interface.
- **E2B / cloud sandboxes.** Already partly available as
  `executor.cloud_preset` (modal, runpod, daytona). They are good for heavy
  or GPU jobs, but they add network round trips and send data to a third
  party, so they don't suit a privacy-first default.
- **Docker Engine SDK instead of the CLI.** Would pull a large dependency
  tree into a single static binary, and make it harder to switch to Podman
  or nerdctl. The CLI is the stable contract.
