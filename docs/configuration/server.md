# Server Configuration

Controls the HTTP server that exposes the REST API and channel webhooks.

## Reference

```yaml
server:
  host: 127.0.0.1      # bind address (default: 127.0.0.1)
  port: 18789          # port to listen on (default: 18789)
  api_key: "sy_..."    # master server API key (required)
  tls_cert: ""        # optional certificate path (both TLS fields required)
  tls_key: ""         # optional private-key path
  published_files: []  # optional per-agent, read-only output folders
  safe_undo:
    resources: []      # optional reviewed conditional-write integrations
  discovery:
    enabled: false    # opt-in LAN discovery, off by default
    interface: ""     # required when enabled, e.g. en0 or eth0
    hostname: ""      # required stable, unique name, e.g. office-soulacy.local
    name: ""          # optional display-name prefix, defaults to Soulacy
```

## Options

For reviewed external record/document changes, see [Safe Undo setup and
integration requirements](../SAFE_UNDO.md). It is disabled until exact,
conditional-write resources are explicitly configured.

### `host`

IP address or hostname to bind. Use `0.0.0.0` to accept connections on all interfaces, or `127.0.0.1` to accept localhost only.

### `port`

TCP port the HTTP server listens on. Default: `18789`.

### `api_key`

The master API key for the server. Clients must send this in the `Authorization: Bearer <key>` header to access protected endpoints. Generate a strong random value:

```bash
openssl rand -hex 32
```

### `tls_cert` / `tls_key`

Provide both paths to enable HTTPS. A missing or invalid certificate/key is a startup error, not a fallback to HTTP. Behind a TLS-terminating reverse proxy, leave both empty and restrict access to the backend listener.

## Example: local dev

```yaml
server:
  host: 127.0.0.1
  port: 18789
  api_key: "dev-only-key"
```

## Example: production with TLS

```yaml
server:
  host: 0.0.0.0
  port: 443
  api_key: "sy_<strong-random-value>"
  tls_cert: /etc/letsencrypt/live/example.com/fullchain.pem
  tls_key: /etc/letsencrypt/live/example.com/privkey.pem
```

## Nearby gateways on iOS (Bonjour)

Advertising is **off by default**. For a trusted LAN, explicitly choose a
private bind address, its network interface, and a stable `.local` hostname:

```yaml
server:
  host: 192.168.1.20        # replace with this server's private LAN address
  port: 18789
  api_key: "<strong-random-value>"
  discovery:
    enabled: true
    interface: eth0        # macOS commonly uses en0 or en1; check the actual NIC
    hostname: office-soulacy.local
    name: Office Soulacy
```

In Soulacy iOS, open Settings → Add Gateway → Find nearby gateways. Selecting a
result fills in the address; it does not pair, authenticate, or grant device
permissions. Verify the address before providing credentials. HTTP exposes
credentials and traffic to the network: use it only on a trusted isolated LAN.
For HTTPS, configure `tls_cert` and `tls_key`; the certificate must match the
discovery hostname and its issuing CA must be trusted by the phone. Discovery
never installs trust exceptions or disables normal TLS verification.

The gateway advertises `_soulacy-gw._tcp.local.` only after the HTTP/TLS socket
binds successfully. Effective authentication is mandatory even if
`allow_unauthenticated` is set. Invalid discovery configuration or unavailable
multicast prevents startup. Only private addresses on the named, up,
multicast-capable, non-loopback interface are published. Wildcard binds publish
only their explicit IP family; a concrete private bind address is preferable.
TXT includes only `scheme` and `protocol=1`, not credentials, user names, machine
names, workspace paths, or capabilities. Display names accept 1–42 ASCII
letters, numbers, spaces, underscores, and hyphens and receive a random suffix.
The configured hostname stays stable across restarts, so saved profiles work.
Choose a hostname unique on the LAN; automatic hostname conflict negotiation is
not provided.

The hostname must be reserved for this gateway, not reused from another
publisher (including an operating system's existing Bonjour hostname). The
advertiser supplies explicit negative address records for the unused IP family
so dual-stack clients do not stall waiting for an address that does not exist.

DNS-SD is unauthenticated and local-link only. It does not traverse a VPN,
cloud network, subnet boundary, or ordinary Docker bridge automatically. Use a
manual HTTPS/tunnel URL for remote/proxied gateways; discovery advertises the
actual backend listener, not an inferred reverse-proxy URL. No firewall,
container-network, or production settings are changed for you. Multicast UDP
5353 and the gateway TCP port must already be reachable on the chosen LAN.

Interface addresses are snapshotted at startup: restart the gateway after an
interface/address change. Shutdown stops answering queries; peer caches may
retain a suggestion for up to the DNS record TTL (120 seconds). The native
client caps results/resolutions at 64, discards stale search callbacks, rejects
malformed/non-local targets and ambiguous TXT, and never uses a TXT `id` as a
trusted identity.

Environment equivalents are `SOULACY_SERVER_DISCOVERY_ENABLED`,
`SOULACY_SERVER_DISCOVERY_INTERFACE`, `SOULACY_SERVER_DISCOVERY_HOSTNAME`, and
`SOULACY_SERVER_DISCOVERY_NAME`. Changes take effect on gateway restart.

## Published files on web and iOS

This browser is disabled by default. To share an agent's output files, create a
dedicated folder and configure it explicitly in the server's `config.yaml`:

```yaml
server:
  published_files:
    - agent_id: research-assistant
      root: /srv/soulacy-published/research-assistant
```

Restart the gateway, then open the agent's **Published files** view in Soulacy
web or iOS. The agent must exist and the signed-in user must have permission to
read it. Effective gateway authentication is required, including on localhost.
No production folder is selected or enabled automatically. There are no file
upload, edit, delete, shell, or arbitrary download endpoints in this feature.

Use a dedicated **local disk** output directory. Keep the root and its parent
directories operator-controlled, and do not point it at the home directory,
soulspace, another agent's private folder, or directories containing credentials,
configuration, databases, or private source-control material. Everything readable
inside the explicitly shared folder should be safe for readers of that agent.
Content is not automatically redacted; a secret inside an ordinary `.txt` file
would still be shared. The server does not copy or move files into the folder.
An agent may write there only if its existing tool/path policy separately allows
it. Publishing a folder does not grant new agent tool permissions.

The implementation on Linux and macOS opens each path component relative to a
directory handle with no-follow semantics. Traversal, hidden path components,
symbolic links and multiply-linked files are rejected. Other server platforms
fail closed until they have an equivalent implementation. At most 64 agent
folders can be configured. Each listing inspects at most 513 entries and returns
at most 512; large directories show an explicit truncation notice, not a false
claim that all files were listed. Split large collections into smaller folders.

Text previews are limited to 128 KiB of valid UTF-8, with no NUL bytes, and an
allowlist of text/source extensions. HTML is displayed as text, never executed.
Each preview includes the byte count and a SHA-256 of the returned content, and
responses use `Cache-Control: no-store`. Files changed during a read may require
a refresh. This is a live read-only browser, not a versioned file snapshot or a
full-system workspace explorer.

Endpoints, relative to `/api/v1`:

- `GET /agents/:id/files?path=` lists the published root or a relative subfolder.
- `GET /agents/:id/files/preview?path=reports/summary.md` returns a text preview.

Query paths use normal URL encoding exactly once. Absolute server paths are
never accepted from clients or included in these responses. iOS also validates
folder identity, entry bounds, byte counts, and preview hashes. Changing gateway
connections discards old native previews; changing web sign-in clears web previews.

iOS displays `.mmd` and `.mermaid` previews as offline diagrams. Chat also renders
closed `mermaid` and `xychart` fences, with expansion and a readable source view.
The bundled renderer supports standard diagrams without downloading scripts,
images, fonts, or icons. It blocks network requests, navigation, callbacks and
embedded HTML. Per-diagram configuration/front matter is disabled; 16 KiB/240
lines, 200 edges and a ten-second deadline bound rendering. At most four diagram
blocks are rendered per message; excess/incomplete fences remain readable code.
Invalid or unsupported diagrams retain a source fallback. Physical-device
performance, accessibility and background behavior still need device QA.
