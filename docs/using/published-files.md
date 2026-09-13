# Read an agent's published output files

**Useful for:** opening a finished Markdown report or text artifact from the web
or iPhone without exposing the server's entire filesystem.

Published files is a **read-only, authenticated folder browser**. It does not
generate reports, grant the agent permission to write them, upload documents,
edit files, or create a public sharing link.

## Operator setup

1. Create a dedicated local output directory, owned/controlled by the operator.
   Do not use a home directory, workspace/config root, or a credentials folder.
2. Create an innocuous `welcome.txt` there using your normal editor with the
   content `Published output test — no private data`.
3. Add the following to the existing server configuration, using a real agent
   ID and the absolute directory path on the **server**:

    ```yaml
    server:
      published_files:
        - agent_id: notes-assistant
          root: /srv/soulacy-published/notes-assistant
    ```

4. Arrange a safe gateway restart after active work has drained. Do not replace
   the rest of `server:` or drop its authentication settings when adding this block.

For containers, the path must exist inside the gateway container and be mounted
intentionally. A path on the operator's laptop is not automatically available
inside a remote server/container.

## Verify the narrow exposure

In the web agent view or **iPhone → Agents → your agent → Published files**,
open the folder and preview `welcome.txt`. You should see the exact test text
and a read-only label. Confirm that another agent without a configured folder
does not expose this folder.

Only approved local directories are browsed. Hidden files, symlinks, unsafe
paths, unsupported file types, and oversized content are not a shortcut to
arbitrary server access. Keep private files out of the directory regardless of
these protections.

## Use it for a real report

Have your separately authorized report-generation process place a reviewed
UTF-8 `.md` or `.txt` file in this directory. Refresh the browser and compare
the filename, size, and content with the server copy. On web, previews are inert
text; iOS can additionally render supported Mermaid diagrams without turning
the file into arbitrary executable HTML.

The current browser caps a listing at 512 entries and a preview at 128 KiB.
Split large report collections into subfolders, and provide a small text summary
for an unsupported or large artifact instead of broadening access.

## If it does not appear

| Symptom | Check |
|---|---|
| Unavailable | Gateway version, configured resource, authentication, and restart |
| Empty folder | Exact server/container path and whether the producer actually wrote a file |
| Preview unavailable | Supported text type, size, and UTF-8 content |
| Permission denied | Agent-read scope and filesystem ownership; do not make the whole workspace world-readable |
| Old file content | Refresh after the producer finishes; inspect the actual file rather than relying on chat claims |

See [server configuration](../configuration/server.md#published-files-on-web-and-ios)
for the complete filesystem contract and supported-platform limits.
