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

The built-in adapter rejects non-loopback Open Notebook URLs. You can reach Soulacy from another device through your normal private-network setup, while Open Notebook's API remains bound to the Mac.

## Connect it

Open **MCP Servers** in the Soulacy GUI. The Open Notebook card checks `http://127.0.0.1:5055` and shows whether the API and MCP bridge are ready. When Open Notebook is healthy, click **Connect Open Notebook**.

The CLI performs the same registration:

```bash
sy mcp add-open-notebook
```

Use a different loopback port when needed:

```bash
sy mcp add-open-notebook --url http://127.0.0.1:5056
```

The command stores an absolute path to the installed `soulacy` binary and runs its embedded adapter. It does not install a Python or Node package and does not require a persistent shell.

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
- **Registered but disconnected:** use **Check again**, then inspect the `open-notebook` row on the MCP page. Reconnect from the card after updating Soulacy.
- **Ask requires model IDs:** call `open_notebook_list_models` and use IDs whose type is `language`.
- **Podcast requires profile IDs:** call `open_notebook_list_episode_profiles` and `open_notebook_list_speaker_profiles` first.
- **Authenticated page fetch fails:** verify the domain grant on the authenticated website connection. Send retrieved text to Open Notebook rather than giving it cookies.
