# Open Notebook

Soulacy can use a local [Open Notebook](https://github.com/lfnovo/open-notebook) instance as an agent toolset. The integration supports notebooks, URL and text sources, source processing status, search, question answering, notes, and podcast generation.

The connection stays on the Soulacy host:

```text
iPhone / Chat / Schedule
          ↓
       Soulacy
          ↓  local MCP process
 Open Notebook API on 127.0.0.1:5055
```

The optional standalone adapter rejects non-loopback Open Notebook URLs. It is installed and upgraded separately from Soulacy, and it does not need to be included in a Soulacy deployment. You can reach Soulacy from another device through your normal private-network setup, while Open Notebook's API remains bound to the Mac.

## Connect it

First install the standalone adapter on the machine that runs the Soulacy gateway. From this repository:

```bash
make install-open-notebook-mcp
```

You can also install it directly with Go:

```bash
go install github.com/soulacy/soulacy/cmd/open-notebook-mcp@latest
```

Make the resulting executable available on the gateway's `PATH`, or place it beside the `soulacy` executable. The normal `make install`, installer, container image, and Soulacy release archives do not include it.

Then open **MCP Servers** in the Soulacy GUI. The Open Notebook card checks `http://127.0.0.1:5055` and reports the API, standalone adapter, and Soulacy connection independently. When both local components are healthy, click **Connect Open Notebook**.

The CLI performs the same registration:

```bash
sy mcp add-open-notebook
```

Use a different loopback port when needed:

```bash
sy mcp add-open-notebook --url http://127.0.0.1:5056
```

The command stores an absolute path to `open-notebook-mcp`. Soulacy starts that executable as a standard stdio MCP process when it connects and after gateway restarts. The adapter remains an independently installed optional component.

## Play podcasts on an iPhone

By default, `open_notebook_get_podcast_audio` returns a loopback URL because the Open Notebook API is private. For a phone connected to the same Tailscale tailnet, run the adapter's media-only proxy on loopback and publish that listener with Tailscale Serve:

```bash
tailscale serve --bg --https=8443 http://127.0.0.1:18791

sy mcp add-open-notebook \
  --audio-base-url https://your-mac.your-tailnet.ts.net:8443 \
  --audio-listen 127.0.0.1:18791
```

Use the MagicDNS hostname shown by `tailscale status`. The same values can be saved from the Open Notebook card on the **MCP Servers** page.

This creates two separate paths:

```text
Soulacy → 127.0.0.1:5055                 full Open Notebook API
iPhone  → Tailscale HTTPS → 127.0.0.1:18791  podcast audio only
```

The media proxy accepts only `GET` and `HEAD` requests for generated podcast audio and forwards byte-range headers for playback and seeking. It returns 404 for notebooks, sources, generation, health, and all other API routes. The listener rejects wildcard, LAN, and public bind addresses.

Keep the iPhone connected to Tailscale. Use `tailscale serve`, which is tailnet-only; do not use `tailscale funnel`, which publishes the endpoint to the public internet. Open Notebook itself can remain bound to `127.0.0.1:5055`.

## Give an agent access

The server ID is `open-notebook`. Add it to an agent's explicit MCP allowlist:

```yaml
mcp_servers: [open-notebook]
```

For tighter access, allow individual tools instead:

```yaml
mcp_tools:
  - mcp__open_notebook__open_notebook_list_notebooks
  - mcp__open_notebook__open_notebook_add_text_source
  - mcp__open_notebook__open_notebook_search
  - mcp__open_notebook__open_notebook_generate_podcast
```

The first release does not expose delete operations.

Scheduled agents that add sources run without an approval screen. After limiting
the agent to the required Open Notebook tools, set `unattended: true` so those
known writes can pass the confirmation guardrail. Keep interactive or broadly
privileged agents attended.

## Authenticated website to podcast

Use a Soulacy authenticated website connection for pages that require a login. The agent should retrieve only content covered by its connection grant, then pass the resulting text to `open_notebook_add_text_source`. Open Notebook does not receive browser cookies.

A typical run is:

1. Retrieve the permitted HBR article through the agent's authenticated `hbr.org` connection.
2. Call `open_notebook_list_notebooks` and select or create the research notebook.
3. Call `open_notebook_add_text_source` with the article text, title, and notebook ID.
4. Call `open_notebook_get_source_status` with `wait_seconds` (up to 300) when asynchronous processing is enabled, so one bounded call waits for the source to finish. Calls without `wait_seconds` remain immediate status checks. Open Notebook reports a blocked or paywalled page as `completed` even though it stored only a short stub; the adapter checks the stored text and returns `status: failed` with `failure_reason: thin_content` (plus `content_chars` and `usable`) for a source under 600 characters, so the agent skips it or adds the article text with `open_notebook_add_text_source`.
5. Search or ask questions. Call `open_notebook_list_models` first when model IDs are unknown.
6. Call the episode-profile and speaker-profile list tools, then `open_notebook_generate_podcast`.
7. Call `open_notebook_get_podcast_job` with `wait_seconds` (up to 300) to wait for completion in one bounded tool call, then read the compact finished-job metadata or its configured audio URL. Calls without `wait_seconds` remain immediate status checks. Full transcripts and outlines stay behind `open_notebook_get_podcast_episode` so routine polling does not consume the agent's context.

`open_notebook_list_podcast_episodes` returns compact episode metadata for
date/name/status checks. Use `open_notebook_get_podcast_episode` only when the
full transcript, outline, or expanded profile data is needed.

For public webpages, `open_notebook_add_url_source` can ask Open Notebook to fetch the page directly. The adapter blocks private, loopback, link-local, and cloud-metadata source URLs.

## Troubleshooting

- **Open Notebook not detected:** confirm its health endpoint responds at `http://127.0.0.1:5055/health` on the Soulacy machine.
- **Adapter not installed:** install `open-notebook-mcp` separately and place it on the gateway `PATH` or beside the `soulacy` executable.
- **Registered but disconnected:** use **Check again**, then inspect the `open-notebook` row on the MCP page. Reconnect from the card after updating either component.
- **Ask requires model IDs:** call `open_notebook_list_models` and use IDs whose type is `language`.
- **Podcast requires profile IDs:** call `open_notebook_list_episode_profiles` and `open_notebook_list_speaker_profiles` first.
- **Podcast opens `127.0.0.1` on a phone:** configure `--audio-base-url` and `--audio-listen`, then point tailnet-only Tailscale Serve at the listener as shown above.
- **Authenticated page fetch fails:** verify the domain grant on the authenticated website connection. Send retrieved text to Open Notebook rather than giving it cookies.
