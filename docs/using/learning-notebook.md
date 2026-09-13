# Learning Notebook

The Notebook lets you teach a bounded lesson, review its sources, and decide
whether an agent should use it later. Start with the complete
[report-format exercise](../use-cases/teach-a-preference.md).

## Where to find it

Web: **Brain Mem → Lessons → choose an agent**. iPhone:
**Agents → choose an agent → Learning**. Enable **Learning Notebook** in the
saved agent first. Learning-related reads and writes require authorized access.

## The lifecycle

1. **Draft:** explicit Teach input or an enabled agent's proposal creates a
   candidate, with source quotations. No candidate means nothing was saved.
2. **Review:** inspect the actual content, source, type, and limitations.
3. **Approve:** only approved guidance becomes eligible for future use.
4. **Test:** start a fresh chat with the same agent/owner; inspect the result.
5. **Disable or refine:** remove unhelpful guidance from future runs. Restoration
   is another draft and another review—not instant reactivation.

Preference/fact guidance can be supplied to a future run; procedural skills
can be retrieved on demand. Retrieval is bounded, not a promise to load every
lesson into every prompt. Search is weighted keyword matching, so wording matters.

## Which kind of memory should I use?

| Need | Mechanism | Important distinction |
|---|---|---|
| “Remember the format I prefer” | Notebook preference | Explicit user preference, reviewed before use |
| “Reuse a verified procedure” | Notebook skill | A bounded procedure with evidence and pitfalls, not arbitrary installed code |
| “Search these manuals” | [Knowledge base](knowledge.md) | Source documents can be updated and retrieved; not permanent personal policy |
| “Continue this conversation” | [Chat session](chat.md) | Old conversation context can still contain a disabled lesson |
| “Inspect broader stored memory/rulebooks” | [Agent memory](memory.md) | Separate storage and review mechanisms |
| “Learn from Studio repairs” | [Studio learning](../studio-learning-memory.md) | Workflow authoring/repair patterns, not this private Notebook |

## Controls that are easy to misunderstand

- **Propose useful lessons:** permits proposals during suitable runs. It never
  means automatically approve them. Off still allows explicit Teach/review.
- **Loaded count:** the guidance was made available; it is not an effectiveness score.
- **Helpful / Needs improvement:** feedback for that lesson, not a self-modifying prompt.
- **Disable lesson:** excludes future reads/runs; cannot erase already-sent context.
- **Draft restoration:** creates a reviewable version; does not grant new tool access.

## Privacy and limits

Private lessons are scoped to an agent and authenticated owner. Sharing one key
shares an identity; issuing a different managed key may create a different
Notebook scope. Do not treat this as personal isolation between users sharing
an admin key.

The local database is permission-restricted but not encrypted by this feature.
Approved content may be sent to the agent's configured model. Do not store
credentials or assume cloud inference stays on your server. A source quotation
proves provenance, not factual truth; facts may become stale.

For exact YAML/API fields, retention, source eligibility, and the boundaries of
the Hermes-inspired learning cycle, see [the technical reference](../LEARNING_NOTEBOOK.md).
