# Deliver a morning briefing to your iPhone

**Useful for:** a project status digest you can read without opening a laptop.
Test three separate things: producing the right report, delivering it to the
right inbox, and getting a push alert. One passing does not prove the others.

**Before starting:** a paired [iPhone](../getting-started/iphone.md), a reachable
gateway that stays on, and a working read-only agent. For live data, you also
need a configured source connector or a maintained knowledge base. A prompt
cannot grant access to your calendar, email, or current news by itself.

## 1. Prove the report with supplied data first

Use your notes agent and this fictional input:

```text
Produce a morning project brief from only these notes.
Format: Summary, Decisions needed, Risks, Sources.
Maya's draft is ready for review. The demo still has no owner.
No launch date has been approved. Source: project notes supplied in this message.
Mark missing or stale information explicitly. Do not send anything yet.
```

Expect a brief that identifies the unassigned demo and undecided launch date.
There should be no invented live status. Inspect the source and tool records.

For a real recurring brief, put the persistent task in the agent's saved
instructions and give it only the read tools/KBs it needs to collect fresh input.
**A cron run does not automatically replay this chat message.** Test that the
agent can collect the intended data when triggered without conversational context.

## 2. Configure a private destination

The mobile channel writes a durable inbox item. Use an exact destination you
have verified, such as `user:<subject>` or `device:<installation-id>`. Obtain
the actual value from your gateway's registered user/device information; do
not use the phone's marketing name or invent an ID.

Merge this fragment into the agent's existing definition; replace the target:

```yaml
trigger: cron
enabled: true
schedule:
  cron: "CRON_TZ=America/Chicago 0 8 * * 1-5"
  run_missed_on_startup: false
  output:
    channel: mobile
    to: "device:REPLACE_WITH_YOUR_INSTALLATION_ID"
```

This schedules weekdays at 8 AM in the stated timezone, with no missed-run
catch-up. Choose your own timezone and verify **Next run** after saving. If you
want catch-up later, understand its latest-only behavior in [Schedules](../using/schedules.md).

!!! warning "Do not test private material with `to: all`"
    `all` is a broadcast destination, not shorthand for “my phone.” Use it only
    when the message is appropriate for every included recipient.

## 3. Check generation and delivery separately

1. In **Schedule**, use **Run** and inspect the output/history. This validates
   generation; a manual run does not prove scheduled delivery.
2. Use **Test output** to send a clearly marked test to the configured destination.
   Open the iPhone **Inbox** and confirm it arrives at the intended device/user.
3. Watch one actual cron fire. Inspect the delivery status and the phone's
   received item. Restore the final cron expression after any temporary test.
4. Disable the test agent when finished if you do not want recurring messages.

## 4. Check push without confusing it with delivery

Allow notifications in iOS and verify the gateway's APNs/relay setup. With the
app backgrounded, wait for a test delivery. If the inbox item appears when you
open the app but no alert appeared, investigate notification permission, Focus
settings, APNs environment, token registration, and gateway push configuration.
Do not regenerate the report repeatedly to debug a missing notification.

Push is a wake-up hint; the durable inbox is the record. See
[Mobile channel setup](../channels/mobile.md) for server-side prerequisites.

## Failure cases worth trying

| Situation | What to verify |
|---|---|
| Source unavailable | Report flags the failure/staleness; no fabricated fresh status |
| Phone temporarily offline | The durable item is available after reconnection; push timing is not guaranteed |
| Wrong destination | Fix the exact registered ID using test-only content; do not broaden to `all` |
| Gateway asleep at 8 AM | No fire with catch-up off; understand the chosen policy before enabling replay |
| Provider budget exhausted | Run fails visibly instead of claiming the report was delivered |

For a daily financial/news brief, additionally verify publication dates and
primary sources. Generated market text is not a substitute for independent
financial verification.
