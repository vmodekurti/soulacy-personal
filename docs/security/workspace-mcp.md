# Workspace MCP security policy

Soulacy treats an MCP server selected by a workspace as untrusted. Team and
Scale workspaces may connect to guarded remote HTTPS MCP servers or install a
repository that publishes a declared OCI, exact-version npm, or exact-version
PyPI MCP package. A supported source-only locked Node or Python MCP package can instead
be built in a disposable Docker builder. Workspaces cannot register a local
command, inherit gateway environment variables, or run package code in the
gateway process.

## Enforced boundary

| Boundary | Team / Scale policy |
|---|---|
| Administration | Workspace owner or admin only |
| Local `stdio` command | Rejected |
| Remote transport | Absolute HTTPS URL only |
| Destination | Public IP space only; loopback, private, link-local, CGNAT, and cloud metadata are rejected |
| DNS | Resolved and pinned by the guarded transport; every redirect is checked again |
| Environment | `env` and `inherit_env` are rejected |
| Credentials | Workspace vault values may be supplied as remote request headers |
| Legacy definitions | Unsafe stored rows are withheld rather than started |
| GitHub installation | Two phase: inspect, then explicit owner/admin approval |
| Source execution | Refused in the gateway; tagged OCI runs directly, exact npm/PyPI packages use disposable runners, and supported locked Node/Python source is compiled in a disposable builder |
| Container identity | Tag is pulled once at approval and persisted as a SHA-256 digest |
| Container boundary | Non-root user, read-only root, no Docker socket, all capabilities dropped, `no-new-privileges`, CPU/RAM/PID/open-file limits |
| Network permission | Admin chooses public API access or no network; host networking is never available |
| Workspace permission | Admin chooses none, read-only, or read/write access to that workspace alone |
| Approval binding | Workspace, approving actor, source commit, image, entry point, 15-minute expiry, and report fingerprint |
| Audit | Inspection and approval identity/revision/digest are recorded |

The URL is checked when it is saved and again by the HTTP transport. Runtime
enforcement is required because a hostname can change its DNS answer after
registration or redirect to an internal service.

## Workspace GitHub installer

The MCP page intentionally exposes only one simple workflow to a Team or Scale
workspace administrator:

1. Paste a public `https://github.com/<owner>/<repository>` URL.
2. Choose whether the server needs public API access and whether it needs no,
   read-only, or read/write access to this workspace.
3. Review the resolved commit, isolated runtime, requested configuration,
   permission summary, and findings.
4. Enter required settings. Declared secrets go directly to the workspace
   vault rather than the MCP registry database.
5. Explicitly approve the fingerprinted report.

Inspection shallow-clones data into a temporary directory, rejects symlinked
metadata and oversized repositories, and never runs repository code. The
manifest's repository identity must match the submitted URL. Soulacy will not
run `npm install`, `pip install`, or repository commands in the gateway
process. An exact-version npm/PyPI declaration may run inside a pinned,
disposable package-runner image. For a supported source-only Node repository,
Soulacy requires `package-lock.json`, disables npm lifecycle scripts, pins the
builder base image, and runs the recognized TypeScript compilation step with
networking disabled. Locked Python repositories require `uv.lock`; Soulacy
installs their frozen dependency graph before copying source, then installs the
project offline with a pinned build backend. Resulting runtime images are
addressed by their SHA-256 image IDs and Python starts with `--offline --no-sync`.

The local Docker runtime provides process/filesystem isolation. Outbound bridge
networking and workspace mounting are explicit, fingerprint-bound permissions
shown before approval; both are disabled or absent when not selected.
High-assurance Scale
deployments should place these containers on the platform's policy-controlled
OCI worker network so egress is also restricted and audited.

## Personal mode

Personal mode retains local `stdio` compatibility because the operator and the
host owner are the same person. Source-based MCP installation is nevertheless
blocked by default: the operator must pass both `--allow-unverified` and
`--allow-host-build`. The second flag is a break-glass acknowledgement that
Python package builds may execute code. Node dependency installation uses
`--ignore-scripts` even after that acknowledgement.

The local `setrlimit` wrapper is a resource guard, not a security boundary. A
Personal-mode local MCP process runs as the Soulacy OS user.

## Remaining artifact hardening

The installer already binds the source commit and immutable image digest. A
future platform catalog milestone should additionally require verified
publisher provenance/signatures, an SBOM, vulnerability policy, reviewed tool
manifest, and a per-server egress allow-list before an artifact can be offered
as pre-approved.
