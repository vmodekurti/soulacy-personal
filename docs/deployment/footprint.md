# Resource footprint and runtime requirements

Soulacy Personal packages the gateway and web UI together, but **“one binary”
does not mean “zero requirements.”** The CLI is separate. Model weights,
workspace data, databases, container layers, and tool environments are separate.
These figures concern the Personal gateway, not a whole-stack capacity guarantee.

## What we have actually measured

On 12 September 2026, the `living-core-20260912` Linux/amd64 release candidate's
**uncompressed gateway executable** was **44,240,496 bytes**: 44.2 MB (decimal),
or 42.2 MiB. It included the production web build and was compiled with Go
1.26.6, CGO enabled, `-trimpath`, and `-ldflags="-s -w …"` to strip debug
information. This is a candidate-build observation, not a size guarantee for
all tagged releases, operating systems, architectures, or future builds.

A development binary can be substantially larger. The local macOS/arm64
development executable inspected in the same review was 87,221,458 bytes.
Do not compare stripped/unstripped executables, compressed archives, and Docker
image sizes as if they were the same measurement.

The previous homepage's **~25 MB**, **idle RAM <30 MB**, and **startup <50 ms**
figures were removed. The packaged candidate does not support the size claim,
and this review has no comparable, reproducible RAM/startup benchmark supporting
the blanket limits. That is not a claim that no configuration could achieve them.

## Which dependencies apply?

| Installation or feature | Requirements |
|---|---|
| Prebuilt gateway + embedded UI | Supported OS/architecture, compatible system libraries, and TLS trust store |
| Browser client | Browser and network access; no separate Node web server |
| AI responses | Reachable provider; local inference needs a model runtime/files, cloud inference needs provider access |
| Python tools | Python and the tool's packages |
| Node-based tools/MCP servers | Node or the runtime required by that integration |
| Containerized tools | Configured container runtime and images |
| Source builds | Go, Node/npm for UI, C compiler/CGO and platform development libraries |
| PostgreSQL/Qdrant deployment | Those services, persistent storage, and their resource budgets |
| iPhone companion | Supported iOS build, reachable gateway, and relevant permissions |

The inspected Linux candidate dynamically links to `libc`, `libm`, and the Linux
loader. The macOS build links to system libraries/frameworks. “No Node or Python
for the prebuilt core” is accurate; “zero dependencies for every feature” is not.
The full Docker image includes optional tooling and is not the gateway's size.

## Measure your deployment

Use a disposable, non-production profile matching your intended configuration.
Record version/commit, checksum, build flags, OS/architecture, CPU/RAM, and
which services the measurement includes.

### Executable size

```bash
# Linux: exact uncompressed gateway size
stat -c '%s bytes' /path/to/soulacy
sha256sum /path/to/soulacy

# macOS
stat -f '%z bytes' /path/to/soulacy
shasum -a 256 /path/to/soulacy
```

### Idle and under-load memory

After initialization, sample resident memory repeatedly while no runs are
active. Repeat under a representative concurrent workload. Record process RSS
separately from container usage, filesystem caches, model memory, and database
processes. One small idle reading is not a capacity recommendation.

For Docker, `docker stats --no-stream <gateway-container>` is a useful snapshot,
not a complete benchmark. Do not silently exclude model/database RAM from a
whole-stack claim.

### Startup and readiness

Time process launch to successful application health/readiness checks for the
configured services—not `--version`, first log output, or merely opening a TCP
socket. State whether the measurement includes container launch, migrations,
warm/cold disk cache, model loading, and external services. Repeat cold and warm
runs; report sample count and range/percentiles.

Health is not proof of model quality or end-to-end task success. Finish with the
[first-run exercise](../getting-started/quickstart.md). Reserve headroom for
concurrency, indexing, and model/provider configuration instead of sizing a
host from a marketing minimum.
