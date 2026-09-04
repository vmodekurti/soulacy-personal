# Skills

Skills are reusable instruction packs — a `SKILL.md` plus supporting files — that any agent can load on demand instead of bloating its system prompt.

## Quick Start

Create a skill directory and attach it to an agent:

```text
~/.soulacy/skills/
  csv-analysis/
    SKILL.md
    scripts/
      analyze.py
```

```markdown title="SKILL.md"
---
name: csv-analysis
description: Analyze CSV files and produce compact summaries.
---

Use this skill when the task involves CSV inspection, cleaning,
aggregation, or chart-ready summaries. Run scripts/analyze.py
for the standard column profile.
```

```yaml title="SOUL.yaml (excerpt)"
skills:
  - csv-analysis
```

That's it — the agent now sees the skill in its catalog and can read the full
instructions when a task calls for it.

## How Skills Work

Skills are **not** YAML fragments merged into the agent. Soulacy injects only
a lightweight catalog (each skill's name and description) into the context.
When the model decides a skill is relevant, it calls:

| Tool | Purpose |
|------|---------|
| `read_skill` | Loads the full `SKILL.md` body for an enabled skill. |
| `read_skill_file` | Reads a supporting file inside the skill directory, e.g. `scripts/analyze.py`. |

These built-ins are only injected when the agent declares `skills:` — agents
without skills pay zero context cost and never waste turns on skill lookups.

## Attaching Skills to an Agent

```yaml
skills:
  - csv-analysis
  - report-style
```

- List specific names, or use `skills: ["*"]` (or `["all"]`) to expose every
  installed skill.
- Empty or absent `skills:` disables the catalog and the skill-reading tools
  entirely.
- In the GUI, the Agents page editor has a **Skills** chip picker populated
  from your installed skills.

## Where Skills Live

The loader scans these locations, in priority order (later wins on name
collision):

1. `~/.agents/skills/` — user-level, cross-client convention
2. The workspace `skills/` directory — Soulacy-native (defaults to `~/.soulacy/skills/`)
3. `<workdir>/.agents/skills/` — project-level, cross-client
4. `<workdir>/.soulacy/skills/` — project-level, Soulacy-native
5. Extra directories from `config.yaml` — highest priority

So a project-level skill overrides a user-level skill of the same name, and
explicitly configured directories override both.

## Hot-Loading

Skills are scanned at startup and rescanned on demand — no restart needed.
Installing a skill through the GUI or the API triggers a rescan
automatically; you can also force one:

```bash
curl -X POST http://localhost:18789/api/v1/skills/rescan \
  -H "Authorization: Bearer $SOULACY_API_KEY"
```

Newly dropped skill directories appear in agent catalogs on the next run.

For installing skills from registries, marketplaces, or archives, see
[Installing Skills](../extend/installing-skills.md).

## Best Practices

- **Make the frontmatter `description` specific.** It is the only thing the
  model sees before deciding whether to call `read_skill` — "Analyze CSV
  files and produce compact summaries" beats "Data helper".
- **Keep supporting files beside `SKILL.md`** — scripts, templates, reference
  examples — and mention them in the instructions so the model knows to fetch
  them with `read_skill_file`.
- **Skills for procedure, tools for capability.** A skill teaches the model
  *how* to do something with what it already has; a
  [Python or MCP tool](tools.md) gives it a new executable action.
- **Enable only what each agent needs.** A focused catalog keeps small models
  from chasing irrelevant skills.

!!! tip
    Skills follow the agentskills.io convention, so skill packs written for
    other agent runtimes (Claude Code, etc.) generally drop straight into
    `~/.agents/skills/` and work unchanged.
