# Connect your iPhone and verify it works

**Outcome:** connect the native Soulacy iOS companion to your gateway, send a
test message, and understand which permissions you have actually granted.

## Before you start

- Install the iOS build made available to you. TestFlight access depends on
  your invitation/testing group; this guide is not a public invitation link.
- Confirm the gateway works in your computer's browser and has a working agent.
- Use a URL the **phone** can reach. `localhost` on the phone means the phone,
  not your laptop. A remote gateway normally needs a trusted HTTPS URL or a
  reachable VPN/tunnel connection.

Do not disable certificate verification to get connected. Check the hostname,
certificate, network, and server instead.

## 1. Pair from the web workspace

1. Sign into the intended gateway in your computer's browser.
2. Open **Mobile**, find **Pair a phone**, and choose **Get code**. Some setup
   instructions call this “Pair a device”; the panel produces the same pairing QR.
3. On iPhone, choose **Connect a gateway → Scan pairing code**. Allow camera
   access for the scanner and scan the QR yourself.
4. Check the gateway address before accepting. Complete the reachability setup
   honestly: local network, VPN/tunnel, or reachable from anywhere.

Pairing codes are short-lived and single-use. Generate a fresh code if one has
expired or was redeemed. Treat the QR as a temporary credential: do not post it
in a ticket or share it publicly. The resulting credential is stored in iOS
Keychain; it does not grant every permission on the phone.

**Checkpoint:** Settings shows the expected gateway address and server version.
Open Agents and verify the expected agent list—not just a successful scan.

## 1a. Pair a phone for someone else in your household

Pairing with no name makes the phone *you*: the same identity as your web
login, so it shares your memory, approvals and inbox. To give another
person their own agents-in-their-pocket, an admin enters their **name** in
**Pair a phone** and chooses **Can run agents** or **View only** before
getting the code. Their phone then signs in as them: separate memory,
separate inbox, and approvals only for what they started. The **Household**
list on the Mobile page shows everyone with a paired phone; **Unpair**
revokes their phone credentials and keeps their memory on the gateway.

A person paired this way has no web login. If they should also use the
web, give them a web account with the same name so the identities match.

## 2. Manual connection and nearby discovery

For another gateway, open **Settings → Manage gateways → Add gateway**. Enter
its reachable URL and the credential issued for it. Prefer an appropriately
scoped credential rather than sharing the server owner's permanent master key.

**Find nearby gateways** is optional. The server must explicitly enable
[LAN discovery](../configuration/server.md#nearby-gateways-on-ios-bonjour), and
iOS must allow local-network access. Discovery suggests an address; it does
not authenticate the server, pair the device, enable tools, or cross arbitrary
subnets/VPNs. If nothing appears, a manual trusted URL is a valid alternative.

## 3. Verify a complete chat

Open your notes agent and send:

```text
Summarize this fictional note: Maya will send the draft on Tuesday.
```

Expect a reply preserving the owner and date. In the web workspace, check
Activity for the same run. This tests the phone → gateway → model → phone path.

To dismiss the chat keyboard, use the keyboard-down button (**Hide keyboard**),
tap the transcript, or scroll it. With a hardware keyboard, Escape also works.
If the control is absent, compare the installed iOS build with the build your
tester group has received. A server upgrade does not install an iOS update.

## 4. Understand the inbox and notifications

Enable notifications when offered, or use **Settings → Notifications** and the
system's notification settings. A server also needs its APNs/relay configuration
for background alerts. An inbox item can be delivered successfully while an
iOS alert is suppressed by permissions or Focus.

Follow [the briefing exercise](../use-cases/morning-brief.md) to verify generation,
inbox delivery, and background push separately. Never paste private report data
into a broadcast test.

## 4a. Let agents reach the lock screen

**Settings → Lock screen** is on by default: a background run appears as a
Live Activity once it starts using tools, and any run appears the moment it
needs your approval, with Approve and Deny right there. **Settings →
Location triggers** needs *Always* location access and only watches places
an agent declared with `trigger: location`.

From any app's share sheet, choose Soulacy to file a document, photo, link
or text into a knowledge base or send it to an agent. Photos are read on
the phone first, so the gateway receives text it can index.

## 5. Grant device access only for a real task

Open **Settings → Device access**. Capabilities start off. Enable only the ones
you understand and need; system permission and Soulacy's own toggle both matter.
The app accepts device commands while it is open. Camera/photo requests require
a visible choice; an agent cannot silently select or capture an image.

Two capabilities are signals rather than actions. **Health summary** lets an
agent ask for steps, energy, exercise minutes, last night's sleep and
workouts over a bounded window; only totals leave the phone, and because iOS
never tells apps what Health access was granted, the toggle itself is your
consent record. **Focus status** reports whether a Focus is on, so an agent
can hold something non-urgent until you are free.

If a command reports uncertain delivery, inspect **Review required → Check
gateway state**. Do not repeat a potentially completed device action blindly.
Pairing is not consent to continuous background monitoring.

## 6. Test offline behavior carefully

With a harmless draft, temporarily disconnect your phone's network. Inspect
**Settings → Outbox** for any queued supported operation, then reconnect and
check its status before sending a duplicate. Safe Undo confirmations, learning
reviews, releases, and other sensitive writes must be performed online; they
are not general-purpose offline jobs.

## If something is missing

| Symptom | First check |
|---|---|
| No TestFlight Update button | You may already have the available build; also check tester group and build eligibility |
| New icon but no new server feature | The iOS app and gateway have independent versions |
| Pairing succeeds, agent list fails | Gateway reachability, credential scope/expiry, and server compatibility |
| Local connection fails away from home | The saved profile is LAN-only; use your configured VPN/tunnel or HTTPS endpoint |
| A feature returns unavailable | It may need a gateway upgrade, resource configuration, or additional authorized scope |
| Lessons differ between web and phone | Verify agent and owner identity; separate managed keys may have separate private learning |

Do not reinstall or delete profiles as your first diagnostic step: you may lose
local drafts or useful evidence. Start with [first checks](../troubleshooting/first-checks.md).
