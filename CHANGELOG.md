# Changelog

All notable changes to this project are documented here. The format is based on
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project aims
to follow [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Fixed
- Text is readable again. The bulk conversion of colour literals to design
  tokens rewrote the token definitions themselves, so each one pointed at
  itself — `--sl-text-faint: var(--sl-text-faint)` — and resolved to nothing.
  Card titles and descriptions fell back to the inherited colour and became
  almost invisible. Nothing caught it: the build succeeded, every test passed,
  and the page still rendered. A test now fails the build if any token is
  defined as itself, does not resolve to a concrete value, or is used without
  being defined.
- The builder stops apologising for its own cut-off replies. It was writing
  the agent's full instructions into every turn, which filled the response and
  truncated the JSON before it finished — so on the very first message the user
  got "Sorry, my answer got cut off" instead of a question. Those instructions
  are now written once, at the end, when everything needed is known, and a
  reply that still arrives truncated is retried with room to finish rather than
  costing the user a turn.
- Secondary actions look like buttons. The `.linkish` class had no style
  anywhere in the app, so seventeen buttons across six pages rendered as bare
  text on a dark background — "Open Studio" and "Change something" were both
  reported as not looking like buttons, which was the correct read. The class
  now has an affordance, and the actions sitting beside a primary button are
  buttons rather than links.
- A new agent's first run no longer shows a wall of raw JSON. Tool results
  reach the model wrapped in an `external_content` envelope that marks where
  untrusted data begins; that wrapper is scaffolding, and leaving it on also
  stopped the payload parsing, so the humaniser fell through to raw text and
  a market-data result was shown to the user as the agent's answer. The
  wrapper is now stripped before anything is displayed, and a built agent gets
  25 turns instead of 10 — at ten it ran out before it could write anything,
  which is what produced the dump.
- The builder stops forgetting what you already told it. Its only memory was
  the raw text of its own previous replies, so one truncated turn poisoned the
  history: in a real session it collected the purpose, the topic and the
  destination, then asked for the purpose again as though the conversation had
  just begun. What has been established is now handed back on every turn as an
  instruction, unusable output is no longer stored as if the assistant had said
  it, and the reply budget is large enough that a reasoning model can finish
  its answer.
- A time you typed is treated as an answer. Saying "7am" left the schedule
  empty and the question was asked again; plain times are now read directly
  rather than depending on the model to emit cron.
- Delivery no longer blocks a build. A first-run user has no channels
  configured, so being asked to name a "channel adapter" was a loop with no
  exit. Results appear in Soulacy unless you ask for somewhere else.
- The reply shown to you is spoken to you. The model leaked its own working
  out into the conversation ("The user has now answered everything: 1. …").
- The Get Started screen sets up a model itself instead of sending you away.
  If nothing usable is connected it holds the request you just typed, offers a
  local model sized to the machine or a cloud key with the place to get one,
  and then replays your request. Previously it showed a banner pointing at
  another page, which is the same dead end in a nicer coat, and a request made
  before that check returned failed several questions later with a raw
  connection-refused error.
- The builder no longer shows raw JSON when the model's reply is cut off. Its
  prompt told the model to begin every `system_prompt` with the full shared
  Operating Contract, which the generator already prepends — so the model spent
  most of its output budget copying boilerplate and was truncated mid-object.
  Nothing parsed, and the fragment was handed back as the assistant's reply, so
  a wall of braces appeared on screen and the conversation could never finish.
  The model is now told the contract is added for it, and a reply that was
  meant to be JSON but did not parse is reported as a cut-off answer rather
  than printed.
- The conversational agent builder produces agents whose tools exist. Its
  prompt promised that deploy would resolve each chosen tool against the live
  catalog; deploy instead wrote `tools/<name>.py` for every tool whatever kind
  it was, with an empty parameter schema and no such file on disk. A built-in
  the conversation correctly identified arrived as a path to nothing, and
  because deploy also skipped validation — the only place those paths are
  checked — nothing caught it. Each name is now sorted into a built-in, an MCP
  tool or a Python tool with its real path, a name matching nothing is
  reported rather than guessed at, and the agent goes through the same
  validator Studio uses before it is written.
- A conversationally-built agent no longer arms a schedule nobody has seen
  run. It is saved enabled so it can be run immediately, but its cron is
  registered only when the caller asks, so the user can watch it work before
  it acts unattended. Deploy also warns when the agent is set to deliver
  through a channel that is not connected, which previously reported success
  and then delivered nothing for as long as it ran.
- A fresh install can answer its first message. Four defects compounded into
  a product that shipped unable to run. The generated config named a ~40GB
  model nobody has, and the built-in fallbacks named another, so boot
  validation disabled every built-in agent and chat replied only "agent is
  disabled". The reason existed solely as a server log line, so the dashboard
  could not show it. And first run wrote the config *after* loading it, so
  even a correct file was invisible to the process that wrote it, which is why
  the first message returned a bare 404. Now: first run detects the models
  actually installed and names the largest one that can hold a conversation,
  writing no model rather than a fictional one when the machine has none; the
  process re-reads what it just wrote, so first boot behaves like every later
  boot; and there are no invented fallback model names anywhere.
- The readiness check actually checks. It asked whether a provider block
  appeared in config, named a model, and carried a key when remote — all
  satisfied by the config the product writes for itself — so a machine with no
  model runtime at all reported "Provider ollama is ready". Since onboarding
  only opens the setup wizard when the provider step is unfinished, the person
  who most needed it was never sent there. It now asks the provider what
  models it has and whether the configured one is among them, and reports both
  with a remedy.
- When boot validation switches an agent off, the dashboard says why and how
  to fix it. The validator already words these well; they just had no way out
  of the log file.
- "Forget everything" on the person model now drops the raw device signals
  too. Without that, the next observer pass re-derived exactly what had just
  been forgotten, so the promise lasted only until the phone next checked in.
  A sectioned purge still keeps the signals: it clears conclusions, and the
  signals go when that sense is switched off.
- `POST /pairing/tokens` (pair a device for yourself) no longer requires
  `config:write`, which only admins hold: an operator phone can now pair its
  Apple Watch. Pairing someone else still requires admin, and a viewer's
  second device stays a viewer.

### Changed
- The System agent now knows its own environment as well as Genie does. It
  could already *list* installed skills, connected MCP tools and peer agents,
  but not read a skill, call an MCP tool or delegate to a peer — so it could
  see that a capability existed and still not use it, and it answered "what's
  installed?" by shelling out to `brew list`. It now holds the same live
  catalogs Genie does, and its prompt tells it to read them before claiming a
  capability is present or absent. This widens nothing: System already has
  `shell_exec`, which subsumes all of it; what changes is that the honest,
  confirmable tool is now the easier path. Every destructive tool still
  requires confirmation, and the production security-readiness report treats
  System's built-in wildcard MCP the way it already treats Genie's — not an
  operator configuration smell, since the loader restores it on every load
  and both are pinned to the `http` channel. An operator's own wildcard
  agent is still flagged.
- The marketing site carries the whole of this cycle's work: a rewritten
  "what's new" (the person model, Getting to Know You, the Steward, triggers,
  senses and consent, provenance, Apple Watch, on-device photo text), and an
  iPhone section reframed as optional with About You, onboarding consent and
  the watch added.
- The core narrative now leads with the assistant rather than the runtime.
  Soulacy is "an assistant that actually knows you", and self-hosting is the
  proof rather than the pitch. Learning is explicitly **by asking** — a
  conversation in the browser, no phone required — with the iPhone app and its
  senses labelled optional throughout, since a person may never install it.
  The front page also says plainly that setup needs a machine and a terminal,
  so nobody arrives expecting a hosted sign-up. Website, README, docs landing
  and the workspace login page.

### Added
- The first run shows what it is doing. A run takes ten to twenty seconds and
  the screen said only "Running it for the first time", which reads as a hang.
  It now lists the steps as they happen — which tool is being used, when the
  result comes back, when the write-up starts — from events the gateway was
  already emitting.
- The palette lives in shared tokens. 594 colour literals across 40 files were
  replaced with the tokens that hold the same values, so a palette change is
  one edit rather than hundreds. Only style blocks were touched, because the
  same hex appears in chart colour arrays in JavaScript where a CSS variable is
  not a colour.
- Get Started is built to the console layout: a workspace line with a live
  count of your agents, the greeting, one card for the thing you want, starter
  cards with a category, and a rail listing the agents you actually have. The
  rail shows real state rather than invented health percentages — a fabricated
  number in a nice font is still a fabricated number. The follow-up screen uses
  the same cards, spacing and type, so answering a question does not look like
  a different product, and Open Studio is a button rather than underlined text.
- The sidebar has a mode: Simple, Standard or Advanced. It listed 26
  destinations and a first-time user met every one of them before doing
  anything, which reads as "this is going to be a lot of work" before the
  product has done anything for you. Simple shows six — Get Started,
  Dashboard, Deployed, Templates, Chat and About You — Standard adds the tools
  of the first week, and Advanced is everything, unchanged. The level is per
  page, so a new screen cannot be added without deciding who it is for, and
  deep links to pages above the current level still work; they are simply not
  listed. The guided tour follows the level too, and no longer opens itself on
  top of the front door: announcing "Step 1 of 28" to someone whose sidebar
  lists six screens was both wrong and exactly the impression the levels exist
  to remove.
- The Get Started screen opens with a greeting rather than a page title, and
  the starters are cards with a name and an example rather than four stacked
  sentences. Shared surface tokens now carry the palette and the corner radii,
  so the geometry can change in one place instead of twenty-six.
- Gateway access keys can be seen, created and revoked from the dashboard.
  The API has supported all three since the beginning and only revoke was ever
  wired up, and then only to clean up after a paired phone. So the key you sign
  in with was minted on first run, echoed to a terminal once, and after that
  there was no way to see which keys existed, add one for a script or a second
  device, or rotate one without hand-editing config.yaml — and someone who
  lost it had no route back in. The new key is shown exactly once, because
  that is the only time the server returns it; listing shows names and
  prefixes and never a secret.
- A front door: describe what you want, watch it run. Soulacy's shortest path
  to a working agent ran through a visual graph editor, and a first-time user
  met 25 screens and a dozen decisions before anything produced a result. The
  conversational builder that solves this had existed on the server for months
  with no screen calling it — its route id was even aliased to Studio, so the
  one word that should have opened a conversation opened the graph editor
  instead. Get Started is now the first item in the sidebar: one question, a
  few follow-ups in plain language, a summary of what will happen in words
  rather than YAML, and then a real run the user watches. A schedule is
  offered only after they have seen the output, and only then is a cron armed.
  Studio is one click away for anything needing branching, code or precise
  tool wiring.
- The builder no longer declares an agent ready that cannot be built. Its
  readiness was entirely self-attested — a confidence score and a `missing`
  list both written by the model — so it would announce "every Friday at 4pm"
  while leaving the cron expression empty, and deploy would then refuse it.
  That reads as the assistant saying yes and the product saying no. Readiness
  now also checks the few things a build genuinely cannot proceed without, and
  any gap goes back into the conversation as a question.
- A local model can be installed from the dashboard. The shipped default
  provider is local, and until now nothing in the product could obtain a model
  for it: the first-run config comment, the validator's remedy, the provider
  doctor and the Providers empty state all told the user to go and run
  `ollama pull` in a terminal. For someone whose only surface is the browser
  that was a dead end on day one, and it is a large part of why a fresh
  install often cannot answer a single message. The Providers page now
  suggests models that fit the machine's actual memory, shows the real
  download size of each, and installs one with a progress bar. The download
  is a job, so it survives a page refresh and closing the tab does not cancel
  it. Every suggested name and size was checked against the Ollama registry
  rather than recalled, because a suggestion that 404s reproduces the exact
  dead end this removes.
- Soulacy can be set to start on login from the dashboard. This was the
  single most consequential first-day setting hidden behind a terminal: with
  no autostart the gateway dies with the window that launched it, every
  scheduled agent stops, and the failure is silent and usually overnight. A
  dashboard-only user could neither see nor fix it. The dashboard now shows
  the state, warns when it is off, and turns it on in one click. The launchd
  and systemd logic moved to `internal/service`, which `sy daemon`, `sy
  doctor` and the gateway all share, so the terminal and the browser cannot
  drift apart about what "installed" means.
- Studio understands the iPhone. It could already name the device tools, since
  its catalogue comes from the live engine, but it could not recognise a
  request for them: "brief me when I get to the office" produced a manually
  triggered agent with no device access and no region, which fails the moment
  it runs. Studio now reads phone vocabulary ("how I slept", "today's
  calendar", "where I am"), infers `location` and `person` triggers from
  arrival, departure, deadlines, state and routine wording, states the access
  plainly in the spec panel including the switches the person must enable, and
  requires the opt-in builtins on the first generation attempt so the saved
  agent actually carries them. A location trigger with no named place is a
  blocking question, because coordinates cannot be guessed.
- **Person triggers**: an agent can run because something about the person
  changed rather than because a clock fired. `trigger: person` with
  `when: state.changed | commitment.due | routine.deviation`. Three guards stop
  it firing constantly: it compares model snapshots rather than writes, a
  condition already true does not fire again, and a per-agent per-person
  cooldown absorbs the burst of observations a phone delivers after being
  offline. An agent that replies with nothing produces no notification.
  See `docs/using/person-triggers.md`.
- The **Steward**: an agent that reads the person model, works out what is
  different about today, and proposes at most three things, each with its
  reason and the lines it read. A quiet day gets a quiet answer. It refuses to
  brief from an empty model, pointing at Getting to Know You instead of
  guessing. Installed on new gateways alongside Getting to Know You. See
  `docs/use-cases/steward.md`.
- A **commitments observer**: what the person is on the hook for, from the
  reminders their phone already holds, so an agent can raise something due
  without the app being awake and in the foreground. A finished reminder
  expires immediately, which reaches devices as a tombstone.
- Starter agents: a new installation now has **Getting to Know You** under
  Deployed rather than an empty list and a Templates page to shop in. It is an
  ordinary editable agent on disk, installed once and recorded, so deleting it
  after onboarding keeps it deleted and a starter added in a later release
  still reaches an existing installation. (`system` and `genie` remain
  built-ins and need no file.)
- An **About You** page in the web app: every line the person model holds,
  grouped and labelled with where it came from ("you told us" with the quote,
  "guessed by" an agent, "noticed by" a sense). Guesses are marked as guesses.
  The sense switches live here with their purposes, alongside a box to add or
  correct a line by hand and a button that forgets everything.
- A **Getting to Know You** agent template that fills in the person model by
  asking rather than by watching: it reads what is already known, asks only
  about the gaps, records each answer with the person's own words, and stops
  after six questions. Its only tools are the person model.
- `person.observe` now takes a `quote` and checks it against what the person
  actually said. An invented quote is refused; a claim of certainty with no
  quote is kept as a guess. This closes the one gap the precedence rule could
  not: a model answering its own interview question and recording the answer
  as fact. Quoted entries read as "they told us" rather than as inferences.
- Person model observers: `state` (driving, moving, in a Focus, resting, from
  Focus/motion/sleep signals) and `routine` (the usual shape of a weekday from
  arrivals and departures, plus how far today departs from it). Devices push
  raw signals to `POST /person/observations`; the gateway digests them with
  plain rules rather than an agent. Every sense is off until switched on at
  `PUT /person/senses/:sense`, each switch states its purpose, and switching
  one off forgets what it concluded and the signals it collected.
- The person model (`internal/person`): Soulacy's structured understanding of
  the person it works for — identity, routine, current state, relationships,
  commitments, preferences. Owner-scoped, with a precedence rule that stops a
  sensor or an agent overwriting what the person said, expiring entries for
  anything short-lived, and a change feed shaped like the memory one so
  devices can hold a local copy. New routes under `/person/model` and two
  opt-in agent tools, `person.model` (answers in prose) and `person.observe`.
  See `docs/using/person-model.md` and `docs/PERSONAL_ASSISTANT_PROGRAM.md`.
- `docs/PERSONAL_ASSISTANT_PROGRAM.md`: the build plan for Soulacy as a
  true personal assistant (perceive, understand, act, learn), with the
  person model as slice one and a concrete schema.
- Apple Watch app in the iPhone build (soulacy-ios): complication with the
  pending-approval count, dictated questions read aloud, and an approvals
  list. The watch pairs through the phone with `POST /pairing/tokens` and
  holds its own managed key; no gateway change was needed.
- Chat attachments accept `extracted_text` from the client: a phone reads a
  photo on-device (Vision) and sends the text alongside the file, since the
  gateway has no OCR. Server-side extraction still wins for documents. Image
  uploads no longer store JPEG bytes as the attachment's "text".
- Ask Genie in the web app: a floating button on every screen, as on the
  iPhone app. One question is routed to the best chat-capable agent (a
  gateway Genie when present, otherwise by vocabulary overlap with the
  agents' names, descriptions and tags) and answered in a new Chat thread.
  The button is not shown in Chat itself, where the composer does the job.
- `mobile.command_status` accepts `wait_seconds` (up to 25) and waits for
  the phone to answer instead of reporting "queued" on an immediate read;
  the Phone Brief agent uses it.
- `examples/agents/phone-brief`: a briefing agent that reads sleep,
  calendar and Focus from a paired iPhone through device commands and shows
  the result as a native card, with a use-case page. The tool risk
  classifier now knows the mobile tools explicitly, so
  `mobile.command_status` is no longer mistaken for a shell tool.
- Siri and CarPlay messaging on iPhone: agents are message contacts for
  Siri's send, read and mark-read intents; replies return as communication
  notifications Siri reads aloud; CarPlay lists agents as conversations
  (Apple's CarPlay Communication entitlement, granted 2026-09-14). No
  gateway change; the phone uses the chat API.
- Memory change feed for devices: `GET /api/v1/memory/facts/sync?since=`
  returns the owner's facts changed after a cursor as upserts and
  tombstones, collapsed to their current state, with `next_cursor`,
  `has_more` and a `reset` signal after a purge. The iOS app uses it to keep
  memory on the phone and search it offline.
- Agent-built UI on iPhone: `canvas.present` accepts typed `components`
  (text, checklist, form, chart, metric) that the phone renders natively; a
  form keeps the command running until the person submits and the result
  carries their answers and checklist states. `mobile.invoke` gains
  `expires_in_seconds` (up to 900) for commands that wait on a person.
- Share-to-knowledge from iPhone: `POST /api/v1/knowledge/:kb/documents`
  accepts a `caption` (JSON or multipart) that is stored ahead of the
  content so retrieval finds the sharer's own words, and multipart uploads
  may carry `extracted_text` for files the gateway cannot read itself
  (photos and scans the phone has already run through on-device text
  recognition), which is ingested in place of the bytes.
- Phone signals for agents: `health.summary` and `focus.status` join the
  device-command allowlist, so an agent with `mobile.invoke` can ask a paired
  iPhone (with those capabilities enabled) for steps, energy, exercise
  minutes, sleep and workouts over a bounded window, or whether a Focus is
  on before delivering something non-urgent.
- Live Activities for paired iPhones: background runs appear on the lock
  screen and in the Dynamic Island once they call their first tool, and any
  run appears the moment it needs an approval, with Approve/Deny buttons that
  decide without opening the app. Device registration accepts an ActivityKit
  push-to-start token (`live_start_token`); `POST /api/v1/mobile/activities`
  registers per-run update tokens; the gateway pushes start, throttled
  progress, waiting and end states over APNs (`liveactivity` push type) or a
  relay (`/v1/live`). The approval broker gained an on-resolve hook.
- iPhone as Soulacy's extension, slice 1: approvals are pushed to paired
  phones as actionable, time-sensitive notifications (`SOULACY_APPROVAL`)
  that can be approved or denied from the lock screen; a new `location`
  trigger kind lets an agent run when a paired phone enters or leaves a
  place (`GET /api/v1/mobile/triggers`, `POST /api/v1/mobile/triggers/:id/fire`,
  with `on`, `radius_m`, `device` and `cooldown` enforced on the gateway and
  the reply delivered to the firing phone); mobile pushes carry a category,
  thread id and data payload. The iOS app adds Siri/Shortcuts actions and a
  "What it remembers" memory screen over the existing memory API.
- Automatic updates: release installs check the signed manifest on a
  schedule (`updates.check_interval`, default 6h), download and verify new
  releases, replace their own binaries, prove the new binary runs, and
  restart when no agent run is in flight (`updates.idle_wait`). Failed
  verification rolls back. Containers, read-only installs and source builds
  are notified only. `updates.auto: false` disables installation; the policy
  is hot-applied from Config, and `/system/updates/status` reports mode,
  reason, last applied version and pending restarts.
- Adaptive memory: durable, user-scoped facts (preferences, identity,
  constraints, entities) are distilled from each conversation turn in the
  background, reconciled so newer facts supersede stale ones with an audit
  trail, and injected into the system prompt under `### 🧠 MEMORY &
  PREFERENCES` within a 50-token budget. Hybrid keyword + embedding recall,
  strict (workspace, owner, agent) isolation, per-agent `memory.adaptive`
  opt-out, `/api/v1/memory/facts` management API, and a "What it remembers"
  tab on the Learning page with edit, add, delete, export, and purge.
- Adaptive memory parity: entity relationships (graph memory) extracted with
  facts and recalled into the prompt; contradiction handling retracts facts
  that are no longer true; this-conversation-only ("temporary") facts;
  per-fact expiry dates; a per-fact change history (created, edited,
  superseded, retracted, deleted) that survives deletion; operator extraction
  instructions and custom categories. New endpoints under
  `/api/v1/memory/facts` for history and relations; Learning page gains a
  Relationships view, retracted filter, expiry editing and history.
- Pluggable memory provider: `memory.adaptive.provider: mem0` swaps the
  built-in engine for a hosted or self-hosted Mem0 service, hot-applied from
  Config → Adaptive memory, with automatic fallback to the local engine when
  the provider is unconfigured.

- When an agent runs out of turns before writing its answer, the fallback
  now says so and renders what was gathered as readable "key: value" lines
  instead of raw tool JSON.
- Fixed: device registration from a phone that offered a Live Activity
  push-to-start token was refused (400) because the token was checked
  against the 32-byte APNs device-token length; ActivityKit tokens are
  longer. Notification and push settings saved from the phone did not
  persist while this was the case.
- Rows created under a legacy companion key id (device, node, commands,
  deliveries, adaptive memory) are moved to the owner at startup, so a
  phone paired before identities existed keeps its settings and memory
  after the identity fix.
- Cloud one-click deployments: the release workflow now publishes
  version-pinned CloudFormation and ARM templates to the public deployment
  bucket, so the website buttons pair a tagged image with the matching
  bootstrap instead of `latest` plus `main`. The Azure button reads the
  published copy.
- Cloud bootstrap readiness now requires the gateway to answer HTTP before the
  stack reports complete.
- Azure template: local admin password is generated per deployment with
  `newGuid()` instead of a derivable value; admin username is a parameter;
  default VM size is `Standard_B2s_v2` with B-series options.
- CI validates the ARM template with `arm-ttk`.

## [1.0.0] - 2026-07-17

First public release. Ships the full Cohort A–H productization sweep, the
seven-story Cohort F security stack (untrusted-content envelope → intent gate →
Security Doctor), Studio "Debug in Studio" run-repair loop, Learning evidence,
Package v2 with install-time secret gate, credential-backed E2 UAT harness, and
the workspace-scoped intent-gate default. `make production-parity` passes 13
required checks in ~83 s on go1.26.5; the six opt-in checks
(`SOULACY_PARITY_LIVE_CHANNELS`, `SOULACY_PARITY_BROWSER_MCP`,
`SOULACY_PARITY_BROWSER_RENDER`, `SOULACY_PARITY_DOCS_SCREENSHOTS`,
`SOULACY_PARITY_STUDIO_LIVE`, plus `.env.uat` credential-backed smoke) run via
`scripts/uat-parity-full.sh`. See `docs/PRODUCTIZATION_REVIEW.md` for the full
per-cohort ledger with file:line evidence.

### Cohort headlines

- **Cohort F — Security stack (S1–S7).** Untrusted-content envelope
  (`internal/trust/`), 14-pattern injection scanner across 8 families
  (`internal/injection/`), tool-call intent gate that composes with capability
  tier + policy + guardrail + ConfirmTools (`internal/intent/`), production
  readiness verdict blocking privileged agents on shared channels
  (`internal/gateway/securityreadiness.go`), 7-fixture red-team regression pack
  (`internal/regression/security_test.go`), Studio pre-save security preflight
  (`internal/studio/security_preflight.go`), per-agent Security Doctor + dry-run
  simulator (`internal/securitydoctor/`, `GET/POST /api/v1/agents/:id/security_doctor`).
- **Cohort F-Bridge — workspace-scoped intent-gate default.** `security.intent_gate`
  flows through runtime, Studio review, and Doctor; per-agent SOUL.yaml still wins.
- **Cohort F-GUI — Svelte wiring** for every F backend surface (Activity chips,
  Agents Security Doctor drawer, Studio security review panel + save-block,
  Dashboard readiness row, Channels privileged-exposure explainer, Config
  intent-gate toggle).
- **Cohort E — Production go-live.** E1 UAT harness harden with per-step timing
  + screenshot gallery, E2 credential-backed smoke harness
  (`scripts/uat-credential-smoke.sh`), E4 provider/cron/hung-run diagnosers, E5
  docs realign to the "local-first agent operating system" framing.
- **Cohort G — Framework-level gap closure.** STARTTLS ordering fix in delivery
  doctor, CI on `codex/**` branches, 30-test Vitest coverage for GUI helpers,
  end-to-end security pipeline scenario test, config schema version stamp.
- **Cohort H — production-parity blocker sweep.** `golangci-lint` clean sweep,
  `.cache`-as-file recovery in UAT + release smoke.
- **Story 7A — Agent Package v2.** Calendar versioning (`YYYY.MM.DD[.PATCH]`),
  namespaced publisher ids, install-time secret gate refusing import when
  required providers/channels/secrets/mcp-servers/peer-agents are missing,
  `.soulacy-package.json` sidecar, `sy package validate <path>` CLI.
- **Cohorts A/B/C — Productization sweep.** Launch readiness scoring
  (`sy launch check/certify/proof`), Studio contract validators for reasoning
  agents (11 checks), Debug-in-Studio failed-run replay + diff-preview repair,
  Channel setup guides + delivery-doctor category catalog, capability-audit
  save-blocking modal, UAT wired into CI with per-step timing report, learning
  evidence with time-window filter + Studio lesson injection, Local-model
  Studio intent presets (`fast_local`/`reliable_local`/`cloud_quality`) and
  streamed/wizard generation pipeline.

### Launch strategy

- New `docs/LAUNCH_STRATEGY.md` — nine-section memo (§1 exec summary; §2 what
  Soulacy has today, cited to file:line; §3 OpenClaw + competitive landscape;
  §4 where "100x better" is real and where it isn't; §5 launch positioning; §6
  ship-for-launch cohort; §7 launch playbook; §8 risks; §9 locked decisions).
  Recommends the "agent framework you can put in production without a security
  memo" positioning wedge.

## [0.9.x] - pre-1.0 hardening

Post-audit hardening from the `post-audit-fixes` branch.

### Breaking changes

- **Destructive system tools default to OFF (SEC-3).** `runtime.allow_system_tools`
  now defaults to `false`. The OS-level built-ins are split into two partitions:
  - **SAFE** (read-only, always available on the local http channel): `read_file`,
    `list_dir`, `find_files`, `fetch_url`, `http_request`, `env_get`, `sys_info`.
  - **SYSTEM** (privileged): `shell_exec`, `run_script`, `install_library`,
    `write_file`, `download_file`. These are offered ONLY when BOTH the server
    permit (`runtime.allow_system_tools: true`) AND a per-agent
    `capabilities: [system]` declaration are present. The legacy
    `system_tools: true` flag is honoured as an alias for the `system`
    capability. Agents relying on shell/file-write access must now set both the
    server flag and the capability. The gateway logs which agents hold the
    `system` capability at startup.
- **Environment allowlist for tool subprocesses (SEC-5).** Spawned Python tool
  processes no longer inherit the gateway's full environment. They receive only
  a base allowlist (`PATH`, `HOME`, `LANG`, `TMPDIR`) plus any variable names
  declared in a per-agent `env: [...]` list. Gateway secrets such as
  `ANTHROPIC_API_KEY` are no longer visible to tool code unless explicitly
  allow-listed.

### Planned breaking changes

- **Auth hard-fails on non-localhost binds with an empty key (SEC-4).** Binding
  to a non-localhost address without an API key will refuse to start unless
  `--allow-unauthenticated` is passed.

### Security / Dependencies (DEP-1)

- Patch-upgraded Go dependencies: `gofiber/fiber/v2` 2.52.4 → 2.52.13,
  `gorilla/websocket` 1.5.1 → 1.5.3, plus transitive bumps (`fasthttp` stack,
  `klauspost/compress`, `mattn/*`). Build + full test suite green.
- `npm audit` (gui): one **moderate** advisory remains — Svelte SSR XSS, fixed
  only in Svelte 5. **Waived**: the gateway GUI is a client-rendered SPA that
  does not use Svelte SSR, and migrating Svelte 4 → 5 (runes rewrite) is a
  separate, out-of-scope effort. Revisit when the GUI moves to Svelte 5.
- `govulncheck` is wired into CI (CI-4); it could not run in the offline build
  sandbox (vuln DB unreachable), so the authoritative scan happens in CI.

### Added

- **Studio "Custom Python" nodes with a per-case consent model.** Studio can now
  author inline Python steps: a `python` flow-node kind (`sdk/reasoning` +
  `internal/reasoning.CompileFlow`), a draggable "Custom Python" palette block, an
  Inspector code editor, and execution via the existing sandboxed process
  executor (`Engine.RunInlinePython` + an inline `run(inputs)` harness in
  `internal/executor/process`). The compiler emits these nodes for glue the
  available tools can't do (e.g. shelling out to a local CLI). Security is
  per-case (docs/STUDIO_PYTHON_TOOLS.md §13): a static classifier
  (`internal/studio/codeclass`) infers `system`/`network`/`dynamic` from the code;
  beyond-guardrail nodes need explicit, content-hash-bound consent collected in
  the save dialog (`internal/studio/consent`, `plan.go`), and the engine is
  **fail-closed** — it refuses to run a beyond-guardrail node without a matching
  grant and the `allow_system_tools` ceiling. Editing the code voids the grant.
  Studio-saved agents also now carry a generated, well-defined system prompt.

### Changed

- **Studio is now built into the core dashboard (ARCH-6).** The visual workflow
  builder is no longer a sandboxed iframe plugin. Its Svelte UI moved from
  `examples/plugins/studio/ui-src` into the main GUI (`gui/src/pages/Studio.svelte`
  + `gui/src/lib/studio/`), is embedded into the gateway binary by `make gui`, and
  is reachable as a first-class route at `/studio` (and `#studio`). The
  host-mediated `postMessage` RPC bridge is gone: Studio now calls the existing
  `/api/v1/studio/*` endpoints directly with the user's authenticated session
  (`gui/src/lib/studio/studioApi.js`). The studio-specific relay was removed from
  `PluginFrame.svelte`, which remains the generic host for other plugin UIs. The
  `examples/plugins/studio` directory was deleted, the Makefile `plugin-ui` target
  and `make all`'s separate plugin build step were removed, and `install.sh` no
  longer copies anything into `<workspace>/plugins/studio`. The `/studio/*`
  endpoints keep their existing user RBAC and are not in the plugin route
  allowlist, so scoped plugin tokens are still rejected. Drafts still persist to
  `<workspace>/studio/drafts/`.
- Data-path write failures (session memory, brain memory, scheduler
  registration, agent upsert) are now logged instead of silently discarded
  (ARCH-1).
- `buildSystemTools` extracted from `internal/runtime/engine.go` into
  per-domain files (`engine_tools_shell.go`, `engine_tools_http.go`,
  `engine_tools_files.go`, `engine_tools_misc.go`) — pure mechanical move,
  no behaviour change (ARCH-2).

### Documentation

- Python SDK marked experimental and no longer advertised as published to PyPI;
  added `sdk/python/README.md` (SDK-1).
- Added `SECURITY.md`, `CONTRIBUTING.md`, and this changelog (DOC-3).

### Fixed

- Python SDK license metadata corrected from MIT to Apache-2.0 to match the
  repository license (DOC-3).
