# Specs & Deep Dives

The repo carries detailed design and contract documents under `docs/`.
These are the authoritative specs the implementation is tested against —
read them when you are building on top of Soulacy (plugins, sidecars,
registries, event consumers) or want the full rationale behind a
subsystem.

## Events & integration contracts

| Spec | One-liner |
|------|-----------|
| [EVENTS.md](../EVENTS.md) | The schema-v1 event envelope, event types, `soulacy.events.*` queue subjects, signed-webhook delivery and compatibility rules |
| [EXTERNAL_CHANNEL_PROTOCOL.md](../EXTERNAL_CHANNEL_PROTOCOL.md) | Run a channel adapter as a stdio sidecar in any language — frame format, handshake, supervision contract |
| [EXTERNAL_STORAGE_PROTOCOL.md](../EXTERNAL_STORAGE_PROTOCOL.md) | Vector/queue backends as JSON-RPC 2.0 stdio sidecars — negotiate, method tables, shared scratch-dir semantics |

## Plugins

| Spec | One-liner |
|------|-----------|
| [PLUGIN_MANIFEST.md](../PLUGIN_MANIFEST.md) | The plugin manifest grammar: identity, entry points, declared capabilities, `sdk_major`, schema evolution rules |
| [PLUGIN_CAPABILITIES.md](../PLUGIN_CAPABILITIES.md) | The capability model — what a plugin may touch (events, config, GUI mounts) and how grants are enforced at runtime |
| [PLUGIN_CREDENTIALS.md](../PLUGIN_CREDENTIALS.md) | How plugins declare and receive secrets from the encrypted vault, with rotation → restart semantics |
| [PLUGIN_MIGRATIONS.md](../PLUGIN_MIGRATIONS.md) | Transactional, checksummed, namespaced database migrations for plugin-owned schemas |
| [PLUGIN_INSTALL.md](../PLUGIN_INSTALL.md) | The stage → introspect → approve install lifecycle, staging directories, and approval fingerprints |
| [SAFETY_INTROSPECTION.md](../SAFETY_INTROSPECTION.md) | The pre-install safety pipeline: static scan, sandboxed dry-run, LLM audit, and the verdict model behind consent prompts |

## Distribution & packaging

| Spec | One-liner |
|------|-----------|
| [PACKAGE_REGISTRIES.md](../PACKAGE_REGISTRIES.md) | The registry provider model (http/git), priority-ordered resolution, the `/v1/search` + `/v1/packages/{slug}` API, ed25519 package signing |
| [CUSTOM_DISTRIBUTIONS.md](../CUSTOM_DISTRIBUTIONS.md) | Building flavored binaries with `soulacy build --with` — compiling third-party drivers into your own distribution |
| [EXTENSIBILITY.md](../EXTENSIBILITY.md) | The umbrella extensibility design: factory registries, SDK seams, and how every extension point fits together |

## Agent behaviour

| Spec | One-liner |
|------|-----------|
| [REASONING_STRATEGIES.md](../REASONING_STRATEGIES.md) | Pluggable reasoning strategies (plan-act and friends), their event trail (`reasoning.*`), and SOUL.yaml opt-in |
| [RULEBOOKS.md](../RULEBOOKS.md) | Versioned procedural memory: how agents learn rules, plus history, rollback, and locking via the brain-memory API |
| [FLOW_GRAPHS.md](../FLOW_GRAPHS.md) | Graph-structured multi-agent workflows — node/edge semantics and the live Flow View |

## Operations

| Spec | One-liner |
|------|-----------|
| [UPGRADE_STABILITY.md](../UPGRADE_STABILITY.md) | The three upgrade guard layers: additive-only schema versioning, pinned API contracts, chaos-tested plugin fallbacks |
| [WORKSPACE.md](../WORKSPACE.md) | The soulspace workspace layout, legacy auto-detection, and the `sy workspace migrate` plan/apply flow |

## Background reading

Not contracts, but useful context alongside the specs:

| Document | One-liner |
|----------|-----------|
| [FRAMEWORK_OVERVIEW.md](../FRAMEWORK_OVERVIEW.md) | Code-level walkthrough of every subsystem with file/line cite points — the best map of the source tree |
| [VOICE_SPIKE.md](../VOICE_SPIKE.md) | The realtime-voice provider comparison and the sidecar-bridge architecture decision behind the [Voice](../configuration/voice.md) feature |
| [TUTORIAL.md](../TUTORIAL.md) | End-to-end hands-on tutorial for building and operating agents |

## Reading order suggestions

- **Writing a plugin?** PLUGIN_MANIFEST → PLUGIN_CAPABILITIES →
  PLUGIN_INSTALL → PLUGIN_CREDENTIALS → PLUGIN_MIGRATIONS.
- **Integrating an external system?** EVENTS first; then
  EXTERNAL_CHANNEL_PROTOCOL (messaging) or EXTERNAL_STORAGE_PROTOCOL
  (databases/queues).
- **Hosting packages for your team?** PACKAGE_REGISTRIES, then the
  `soulacy registry serve` section of the [CLI reference](../cli/reference.md).
- **Operating in production?** UPGRADE_STABILITY and WORKSPACE, plus
  [Upgrades & Reinstall](../deployment/upgrades.md).

!!! note "Specs vs user docs"
    These documents are contracts: they describe exact wire formats and
    invariants, and tests pin them. The user-facing pages in this site
    (Configuration, API, CLI) are the friendlier layer on top — when the
    two seem to disagree, the spec wins and the user docs have a bug
    (please report it).
