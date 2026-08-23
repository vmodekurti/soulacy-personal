# Workspace MCP security policy

Soulacy treats an MCP server selected by a workspace as untrusted. Team and
Scale workspaces may connect to guarded remote HTTPS MCP servers or install a
repository that publishes a declared OCI MCP package. They cannot register a
local command, inherit gateway environment variables, or install a source
package on the gateway host.

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
| Source execution | Refused; the repository must declare a tagged OCI stdio package |
| Container identity | Tag is pulled once at approval and persisted as a SHA-256 digest |
| Container boundary | Read-only root, no host mounts or Docker socket, all capabilities dropped, `no-new-privileges`, CPU/RAM/PID limits |
| Approval binding | Workspace, approving actor, source commit, image, entry point, 15-minute expiry, and report fingerprint |
| Audit | Inspection and approval identity/revision/digest are recorded |

The URL is checked when it is saved and again by the HTTP transport. Runtime
enforcement is required because a hostname can change its DNS answer after
registration or redirect to an internal service.

## Workspace GitHub installer

The MCP page intentionally exposes only one simple workflow to a Team or Scale
workspace administrator:

1. Paste a public `https://github.com/<owner>/<repository>` URL.
2. Review the resolved commit, declared image, requested configuration names,
   network permission, isolation profile, and findings.
3. Explicitly approve the fingerprinted report.

Inspection shallow-clones data into a temporary directory, rejects symlinked
metadata and oversized repositories, and never runs repository code. The
manifest's repository identity must match the submitted URL. Soulacy will not
fall back to `npm install`, `pip install`, a Docker build, or an arbitrary host
command when the OCI declaration is absent.

The local Docker runtime provides process/filesystem isolation and permits
outbound bridge networking because many MCP tools call public APIs. That
network permission is shown as a warning before approval. High-assurance Scale
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
