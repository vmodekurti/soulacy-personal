---
name: writing-in-your-voice
description: Draft or rewrite text so it sounds like the person, not like an AI — emails, messages, posts, bios, replies, thank-yous, tricky notes. Uses what Soulacy knows about them (tone, phrases, sign-off, audience) and asks only for what it cannot know.
license: Apache-2.0
allowed-tools: person.model person.observe semantic_memory_search session_search read_file write_file
metadata:
  builtin: "true"
  category: communication
---

# Writing in your voice

Generic AI prose is easy to spot and the person will have to rewrite it.
Your job is to write what *they* would have written on a good day.

## Before drafting

1. Call `person.model` and pull what matters for writing: how they address
   people, formality by audience, favourite phrases, things they never say,
   sign-off, language/locale, spelling conventions.
2. If the model is thin, look for their own past writing:
   `semantic_memory_search` / `session_search` for messages they wrote, or a
   file they point you to. Match rhythm and length, not just words.
3. Work out **audience** and **goal** from the ask. If either would change
   the draft materially and is not inferable (boss vs. friend; "say no" vs.
   "delay"), ask *one* question. Otherwise state the assumption in one
   line above the draft.

## Drafting rules

- Length: what the person would send. Short messages stay short. One idea per paragraph.
- Open like they do (no "I hope this email finds you well" unless they use it). Close with their sign-off.
- Keep their quirks (a favourite word, dashes, lowercase) — do not "correct" style. Do fix real typos.
- No filler, no hedging stacks, no "I wanted to reach out", no exclamation marks they would not use.
- Hard messages (declining, chasing, apologising): be direct in the first line, kind in the second, concrete about the next step in the third.
- Replies: quote nothing back; answer every question asked; carry over names and dates exactly.
- Offer at most **two** variants only when tone is genuinely uncertain (e.g. "warmer" / "firmer"). Otherwise one draft.

## After

- Deliver the draft as plain text ready to paste, nothing around it except the one-line assumption if any.
- When the person edits or accepts a draft, record what you learned with `person.observe` (e.g. "prefers 'Cheers' with colleagues", "never uses exclamation marks") so the next draft needs fewer questions.
