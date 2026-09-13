# Teach an agent your report format

**Useful for:** avoiding the same formatting correction in every conversation.
This is reviewed guidance stored for an agent and authenticated owner—not
model training, and not a promise the model will always follow it.

**Before starting:** use a learning-capable gateway, a saved agent with a working
model, and an authorized reviewer. Start with a test agent and non-secret text.

## 1. Enable deliberate learning

In the agent editor, enable **Learning Notebook** and save. For this exercise,
leave **Propose useful lessons** off so you choose when a lesson is drafted.
Equivalent fragment:

```yaml
learning:
  enabled: true
  auto_propose: false
  max_proposals: 2
```

If the agent has an explicit built-in allowlist and you want in-chat learning
tools, include `learning.search`, `learning.read`, and `learning.propose`.
The dedicated Teach screen can draft a lesson without a tool-enabled chat.

## 2. Teach one specific preference

Web: **Brain Mem → Lessons**, then select the agent. iPhone:
**Agents → your agent → Learning**.

Open **Teach a preference or workflow** and enter:

```text
For my project status reports, use these headings in order: Summary,
Decisions needed, Risks, Sources. Keep Summary to three bullets or fewer.
If there is no evidence for a claim, mark it as unknown. This is my preferred
report format, not permission to browse, send messages, or change any records.
```

Choose **Create learning draft**. This uses one governed model call and can
cost money. It can also return no suitable lesson; nothing is auto-approved.

## 3. Review the draft, not just its title

In **Needs review**, open the lesson. Check its content, type, source quote,
and limitations. It should be a preference about this format—not a “fact” that
all reports have no risks, or permission to send reports automatically.

Tick **I reviewed this lesson, its sources and limitations**, then choose
**Approve lesson**. If it misrepresents your request, reject it and teach a
clearer, narrower version instead. A citation proves where text came from,
not that a claim is universally correct.

## 4. Test in a fresh conversation

Start a new chat with the **same agent and owner identity**. Do not repeat the
format instructions. Ask:

```text
Write my project status report. Fictional notes: the draft is done;
the demo has no owner; no launch date has been approved.
```

Expect the four headings, a short Summary, and honest unknowns. Inspect the
lesson's load count as supporting evidence that guidance was available, but
judge the actual answer yourself. A loaded lesson is not proof it helped.

## 5. Prove you can stop using it

Open the active lesson under **In use**, review it, and choose **Disable lesson**.
Start another fresh chat. The lesson is no longer supplied to future runs.
An existing conversation or already-running response may still contain the
old guidance, and the model may independently choose the same headings.

**Draft restoration** creates a new reviewable draft; it does not silently
reactivate the lesson. Approve the restoration only if you want it used again.

## Boundary checks

- Another agent or a different managed key/owner should not inherit this private
  lesson. A shared key is a shared identity; it does not isolate people.
- Helpful/Needs improvement is feedback, not automatic approval or rewriting.
- Do not teach passwords, API keys, temporary facts as timeless policy, or
  instructions to bypass safety controls.
- If the model keeps ignoring the lesson, inspect approved content, identity,
  the selected agent, and a fresh run before adding duplicate drafts.

Continue with [Learning Notebook](../using/learning-notebook.md) for memory types,
privacy, retrieval, and limits.
