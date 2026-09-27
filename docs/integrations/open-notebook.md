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

## Authenticated website to podcast

Use a Soulacy authenticated website connection for pages that require a login. The agent should retrieve only content covered by its connection grant, then pass the resulting text to `open_notebook_add_text_source`. Open Notebook does not receive browser cookies.

A typical run is:

1. Retrieve the permitted HBR article through the agent's authenticated `hbr.org` connection.
2. Call `open_notebook_list_notebooks` and select or create the research notebook.
3. Call `open_notebook_add_text_source` with the article text, title, and notebook ID.
4. Poll `open_notebook_get_source_status` when asynchronous processing is enabled.
5. Search or ask questions. Call `open_notebook_list_models` first when model IDs are unknown.
6. Call the episode-profile and speaker-profile list tools, then `open_notebook_generate_podcast`.
7. Poll `open_notebook_get_podcast_job` and read the finished episode metadata or its host-local audio URL.

For public webpages, `open_notebook_add_url_source` can ask Open Notebook to fetch the page directly. The adapter blocks private, loopback, link-local, and cloud-metadata source URLs.

## Troubleshooting

- **Open Notebook not detected:** confirm its health endpoint responds at `http://127.0.0.1:5055/health` on the Soulacy machine.
- **Adapter not installed:** install `open-notebook-mcp` separately and place it on the gateway `PATH` or beside the `soulacy` executable.
- **Registered but disconnected:** use **Check again**, then inspect the `open-notebook` row on the MCP page. Reconnect from the card after updating either component.
- **Ask requires model IDs:** call `open_notebook_list_models` and use IDs whose type is `language`.
- **Podcast requires profile IDs:** call `open_notebook_list_episode_profiles` and `open_notebook_list_speaker_profiles` first.
- **Authenticated page fetch fails:** verify the domain grant on the authenticated website connection. Send retrieved text to Open Notebook rather than giving it cookies.
