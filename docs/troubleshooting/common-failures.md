# Common Failures → Fixes

This page maps the errors you're most likely to hit to their cause and the exact
fix. Soulacy tries to surface these as actionable messages in the GUI and in
`sy doctor`; this is the reference behind them.

Run `sy doctor` first — it checks the vault, provider auth, ports, adapters, and
recent error rate, and prints a specific remedy for each failed check.

## Install & startup

| Message | Cause | Fix |
| --- | --- | --- |
| `command not found: sy` / `soulacy` | Binary not on PATH | Re-run the installer, or add the install dir (`~/.local/bin`, `/usr/local/bin`, `/opt/homebrew/bin`) to PATH |
| `address already in use` | Another process holds the gateway port | `sy doctor` reports the port; stop the other process or change `server.addr` |
| Gateway starts then exits | Config invalid or vault locked | Check `sy daemon logs`; run `sy doctor` |
| Service won't start on login | Daemon not installed | `sy daemon install`, then `sy daemon status` |
| `sy doctor` warns `no update manifest configured` | Production upgrade path is not wired | Set `updates.manifest_url` in `config.yaml` or export `SOULACY_UPDATE_MANIFEST` |
| `sy doctor` warns `update manifest could not be checked` | Manifest URL/file is unreachable or invalid | Verify the URL/file path, JSON shape, and artifact links before launch |
| API key works with `soulacy serve` but not systemd | Foreground and service processes loaded different config files or workspaces | Inspect `User`, `HOME`, `SOULACY_CONFIG_PATH`, and `SOULACY_WORKSPACE`; follow the [VPS verification checklist](../deployment/linux.md#5-prove-the-service-loaded-the-intended-config) |
| `sy update` says versions are not comparable | Installed binary identifies itself by a source commit, not a tagged release version | Install the intended tagged bundle explicitly; do not override the comparison |

## LLM providers (Provider Doctor)

Click **Test connection** on any card on the **Providers** page (or call `GET
/api/v1/providers/:id/models`). When the call fails, the response includes a
structured `diagnosis` — `{category, reason, fix, detail}` — and the GUI
renders `reason` (bold), `fix` (secondary line), and a **Show raw error**
toggle for the underlying provider response.

The diagnosis categories mirror what OpenAI / Anthropic / Google / Groq /
OpenRouter / Together / Mistral / DeepSeek / Grok / Ollama actually return:

| Category | What it means | Fix |
| --- | --- | --- |
| `missing_key` | The provider needs a key and none was sent | Add the key on Providers → *provider*, save to the vault, restart |
| `bad_key` | 401 / `invalid_api_key` / `authentication_error` / rotated or revoked key | Rotate the key on the provider's dashboard; save + restart. The fix line names the correct URL for each provider (`platform.openai.com/api-keys`, `console.anthropic.com/settings/keys`, `aistudio.google.com/apikey`, etc.) |
| `forbidden` | 403 / project or org has no access to that model | Enable access to the model on the provider dashboard, or scope the API key to the right project |
| `region_blocked` | 403 with "country, region, or territory not supported" | Route the gateway through a supported egress or configure a different provider |
| `rate_limited` | 429 / RPM or TPM ceiling from your key | Slow the call rate, raise your tier, or spread agents across multiple keys/providers |
| `overloaded` | Anthropic `overloaded_error` / capacity signals | Wait and retry; consider temporarily failing over to another provider via the Studio model picker |
| `quota_exceeded` | 402 / `insufficient_quota` / no billing | Top up credit or enable billing on the provider's dashboard |
| `model_not_found` | The model id was renamed, deprecated, or your key has no access | Click **List models** to see what's live on this key and re-Save one of them as default |
| `context_too_large` | 413 / `context_length_exceeded` | Trim the system prompt, switch to a longer-context model, or summarise older turns via Memory settings |
| `provider_down` | 5xx / service unavailable | Retry in a minute, check the provider's status page, fail over via the Studio model picker |
| `bad_endpoint` | DNS lookup, TLS handshake, or malformed URL | Verify the `base_url` on Providers (typos in the host are the common cause) and outbound HTTPS |
| `network` | Timeout, connection refused, EOF on the provider socket | Check outbound network access, then retry |
| `local_unreachable` | Ollama / LM Studio / vLLM socket is not answering | `ollama serve` (or your equivalent) and confirm the base URL matches |

For Ollama specifically, `model_not_found` shows the "pull the model first" fix
because the local runtime speaks a different vocabulary from cloud providers.

If you see a diagnosis of `unknown`, the raw error the provider returned was
new to us. Please open an issue with the raw error text so we can add the
category to the doctor.

## Channel delivery (Delivery Doctor)

Each channel mapping has a **Diagnose** button (backed by
`POST /api/v1/channels/:id/diagnose`) that runs these checks and reports the
specific reason in plain language — what happened and how to fix it. Pass
`{"dry": true}` to check readiness (destination set? adapter registered and
connected?) without sending a real message. Common results:

| Symptom | Cause | Fix |
| --- | --- | --- |
| `missing "to"` | No destination on a scheduled/one-off send | Set a destination on the mapping, or let it use the default outbound bot |
| `chat not found` / `invalid chat id` | Wrong Telegram chat ID | Re-copy the chat ID; the bot must have received at least one message in that chat |
| `bot is not a member` / `not_in_channel` | Bot never invited | Invite the bot to the Slack channel / Telegram group / Discord channel |
| `channel_not_found` / `not found` | Slack/Discord destination ID does not match a channel the bot can see | Use the platform-native channel ID (`C...`/`G...` for Slack, numeric ID for Discord) and invite the bot |
| `missing scope` | Slack app lacks `chat:write` | Add the scope in the Slack app config and reinstall the app |
| `unauthorized` / `invalid token` | Wrong or rotated bot token | Update the token in the vault |
| `forbidden` / `permission denied` | Token is valid but cannot post to the destination | Grant post permission or choose a destination where the bot is a member |
| `rate limited` / `429` | Platform throttled delivery | Retry later; reduce schedule fan-out or add backoff |
| Teams / Google Chat returns `404` | Incoming webhook or Workflow URL is wrong/expired | Create a fresh incoming webhook/Workflow URL and save it as `webhook_url` or `default_output_to` |
| Email returns authentication or relay error | SMTP user/password/from address not accepted by the server | Re-test SMTP credentials and verify the configured sender is allowed by the relay |
| `adapter disabled` | Channel adapter turned off | Enable the adapter for that channel |
| `restart required` | Adapter config changed since boot | Restart the gateway (`sy daemon stop && sy daemon start`) |

Rich, GUI-only output (charts, tables, images) is automatically converted to
channel-safe text before delivery, so a message that looks fine in Chat won't
fail silently on Telegram/Slack/Discord.

If the **Test** button fails in **Channels**, the HTTP response includes a
structured diagnosis with `kind`, `summary`, `detail`, and `fix`. The GUI
renders that same diagnosis on the channel card so you can fix the destination
before wiring it into a schedule or workflow.

## Studio & workflows

| Symptom | Cause | Fix |
| --- | --- | --- |
| Refine turns a conversational request into a scheduled job | Trigger was left on Auto or an assumption in the refined text was accepted | Set Trigger to Channel or Manual on the main Studio page, correct the refined prompt, and regenerate |
| Generated provider/model is not one you configured | Execution model inherited a default or the generated YAML was not reviewed | Choose **Model this agent runs on** from the registered catalog, test it on Providers, and verify `llm.provider` / `llm.model` in YAML |
| Studio repeatedly chooses Telegram | Delivery remained on Auto or the draft retained an earlier output channel | Select Reply, None, or the intended channel; set/clear the destination before regenerating |
| Final response appears inside an Action Required dialog | The agent called outbound `channel.send`, which is confirmation-gated | For ordinary conversation use Delivery: Reply and remove unnecessary `channel.send`; approve only intentional outbound delivery |
| Agent contract Goal or Instructions is generic | Sparse intent or deterministic fallback filled missing model output | Add observable success/failure criteria, regenerate, or edit the populated contract before saving |
| Save blocked by integrity check | Dangling reference, missing variable, invalid Python | Read the specific check message; fix the flagged node |
| Run fails with `template variable not found` | A `{{var}}` is used before it's set | Use **Debug in Studio** → it identifies the unset variable and proposes a binding |
| Repair won't converge | The failure needs external input (e.g. a missing secret) | Provide the secret/tool, then re-run **Build until it works** |

## Schedules

| Symptom | Cause | Fix |
| --- | --- | --- |
| Save fails with `schedule.cron: "* * *" is not a valid cron expression` | Save-time cron validation caught a malformed expression (wrong field count, out-of-range value, gibberish) | Use 5 fields (`0 9 * * 1-5`), an @descriptor (`@daily`, `@hourly`), or an optional 6-field form (`0 */30 * * * *`) |
| Scheduled run never fires | Daemon not running | `sy daemon status`; install/start it |
| "Next run" is blank after Save | Rare — the runtime rejected the expression after Save (pre-validation missed a case) | Check the gateway logs for `scheduler re-registration failed`; open the issue tracker with the cron string |
| Scheduled output not delivered | No default outbound bot and no per-schedule destination | Set a default outbound bot, or a destination on the schedule |
| History panel shows fewer runs than expected | Older runs came from another trigger path or the durable event scan hit its cap | Open **Schedule → History** and check the source/truncation note; download a support bundle for the merged action-log + workflow ledger |
| Manual run works but cron delivery fails | The agent can answer, but scheduled output cannot resolve a destination | Use **Test output** on the schedule row; fix the channel diagnosis before waiting for cron |
| An unexpected run fired at boot with an `⟳ auto-replayed` chip on the Automations row | `schedule.missed_run_backfilled` — the gateway was down when a scheduled fire came due, and the most recent missed fire in the `missed_startup_window` (default `24h`) was replayed once at startup | Normal after an outage. If the same agent backfills every restart, either widen `schedule.missed_startup_window` (older fires stop qualifying) or check whether the gateway is crashing between runs |
| Cron fires twice after a restart | Missed-run catch-up and manual testing overlapped | Disable `run_missed_on_startup` or keep the catch-up window narrow for agents you also trigger manually |

## Activity — the "Running now" strip

The **Activity** page polls `/activity/running` every 3s and renders a card per
in-flight session with elapsed time, silent-for, and the last event type. When
a session's silent window crosses 5 minutes the card turns red with a
per-last-event-type reason + fix — for example, an `llm.call` last event says
"waiting on the LLM provider for X — check Providers for rate-limit/overload",
while a `tool.call` last event points at MCP/subprocess wedges. Click any
hung card to jump into the per-session Activity view and inspect the tail.

## When you're stuck: support bundle

`sy support bundle` produces a redacted support bundle — config with secrets
stripped, recent logs, `doctor` output, release/update metadata, versions,
recent failures, admin audit events, and a merged run ledger — safe to share
when asking for help. The live gateway bundle includes:

- `doctor.json`: provider and channel readiness, including delivery checks.
- `readiness.json`: launch-readiness score and blocking checks.
- `browser_status.json`, `mobile_status.json`, and `chat_status.json`: live
  experience readiness for browser automation, phone/PWA operations, and Chat.
- `run_ledger.json`: merged action-log + workflow history, including trigger
  source, delivery status, output preview, event count, and truncation metadata.
- `admin_audit.json`: recent config/security/admin changes.

The same bundle can be downloaded from **Dashboard → Launch Readiness** or
**Config → Support**. It never includes secret values.

If you create the bundle from the CLI while the gateway is offline, it includes
`operator_evidence.json` with the live endpoints to capture after the gateway is
running.
