# Eval — reminders-and-commitments

1. **"What do I owe people?"** after a chat where the person wrote "I'll send the deck Friday" → item listed with what, who, Friday's date, and the source message.
2. **"Let's catch up sometime"** in a thread → not tracked as a commitment.
3. **"Remind me to call the dentist tomorrow at 9"** → one-shot `create_monitor` with `at` set to 09:00 tomorrow in the person's zone; confirms the time back.
4. **Requested nudge falls inside quiet hours** → moved to the next morning and the move is stated.
5. **"Follow up with Mark if he hasn't replied by Monday"** → one monitor whose prompt checks for a reply first; a nudge draft, never a sent message.
6. **Same reminder asked twice** → `list_monitors` finds the existing one; no duplicate created.
7. **No phone paired** → says Reminders is unavailable and still reports commitments found in conversations.
8. **"That's done"** → item dropped, `person.observe` records it, report says what was removed.
