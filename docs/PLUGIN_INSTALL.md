# Plugin Discovery & Install UX (Story E13)

Local-first plugin installation through the GUI (**Plugins** page) or the
API — no central marketplace dependency. The metadata (source, checksum,
approval fingerprint) is designed so a registry/marketplace can layer on
later.

## Flow

1. **Stage** — `POST /api/v1/plugins/install {source, checksum?}`.
   Sources: a git URL (shallow-cloned, history stripped), a sha256-
   checksummed archive (`.tar.gz`/`.zip`; checksum **required**, verified
   before extraction; path-traversal and size hardened), or a local
   directory. Staged plugins live under `<plugins>/.staging` and are never
   loaded.
2. **Approve** — the response is a preview of *everything* the manifest
   requests: capabilities (with scopes; unscoped grants flagged loudly),
   vault credentials, sidecar channels, providers, GUI mounts. The GUI
   renders this as an explicit approval dialog.
   `POST /api/v1/plugins/install/:staged/approve` activates: the plugin
   moves into the plugins root and `.soulacy-install.json` records the
   approved permission fingerprint (canonical, order-insensitive hash of
   permissions+credentials).
3. **Load** — at the next gateway restart the loader's install gate admits
   the plugin. Every response carries the restart note.

## Re-approval on permission changes

If an update changes the manifest's permissions or credentials, the
fingerprint no longer matches the approval: the plugin **stops loading**
(`plugins: plugin skipped by install state` in the logs), the list shows
*needs re-approval*, and `POST /api/v1/plugins/:id/reapprove` (the GUI's
Re-approve button) is the explicit human answer. Reordering grants is not a
change; adding, widening, or re-scoping one is.

## Management

`GET /api/v1/plugins/installed` lists installer-managed plugins;
`POST /api/v1/plugins/:id/enable|disable` flips the load gate without
touching files; `DELETE /api/v1/plugins/:id` removes from disk. All routes
require config-level rbac; plugin principals are default-denied (E8).

**Hand-installed plugins** (directories without install metadata) are
implicitly approved and invisible to the installer — placing files on disk
already required operator access. Nothing changes for existing setups.
