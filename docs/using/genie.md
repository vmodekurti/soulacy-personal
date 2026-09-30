# Talking to Genie

Genie is the native iPhone way in. You ask for something in your own words, and
Genie either answers it or builds the thing that will keep answering it.

Soulacy does not require every request to pass through Genie. WhatsApp,
Telegram, Slack, Discord, email, HTTP, and custom adapters are first class
channels into the same gateway. An agent can receive a request and reply on the
channel that triggered it, and scheduled work can be delivered there directly.
The runtime applies the same permissions, memory, and run records everywhere.

Most of Soulacy (Studio, the agent list, schedules, delivery channels) is
still there, and you will want it eventually. You do not need any of it to
start.

Genie is also the main way to operate Soulacy from the iPhone app. You can ask
for work, turn a repeated request into an automation, watch its progress, and
manage what Genie created without returning to a desktop. The gateway still
runs the agents and enforces their permissions.

## Use Genie from your iPhone

Open Genie in the Soulacy app and ask in ordinary language. For example:

- “Brief me on overnight AI news every weekday at 7.”
- “Watch this page and tell me when the pricing changes.”
- “Research these three companies and put the comparison in my inbox.”
- “What automations have you created for me?”
- “Pause the weekday briefing.”

A one-off request can run immediately. A recurring request becomes a saved
agent with a schedule, permissions, and a visible owner. Genie shows what it
is doing, and completed work lands in the phone's durable inbox. If a risky
step needs an attended decision, Soulacy pauses and asks on the phone; Genie
cannot approve its own request.

## Give Genie an ongoing mission

A mission is a standing responsibility with a visible contract. It records:

- the outcome Genie owns;
- the finish line;
- the recurring or one-time schedule;
- optional delivery details;
- current progress and any blocker;
- the next action;
- the background runner doing the work.

Ask Genie to plan the mission first. Confirm the finish line and schedule, then
activate it. You can inspect every mission on the **Missions** page and pause,
resume, complete, or cancel it at any time. Mission records survive a gateway
restart, while the linked scheduled runner resumes from the saved agent
definition and scheduler state.

A mission is not a new permission. Its runner receives the same bounded tools
as a Genie monitor, and risky actions still go through the existing grant and
approval policy. Mission records contain no API keys, cookies, or website
session material.

Use a mission when you care about an ongoing outcome and want visible progress.
Use a monitor for a narrow condition such as a price crossing a threshold. Use
an agent when you need a reusable worker with a defined workflow.

## Let Genie figure out the approach

For a real-world goal, Genie now checks what Soulacy can actually use before it
claims the work is possible. The approach can combine public web research,
user-created connectors, installed skills, MCP tools, browser automation, and
saved Website Access sessions.

For example, “reserve a table for four on Friday evening” produces an approach
that identifies the missing location, time range, and restaurant preferences.
It then checks for a restaurant booking tool or browser automation, explains
whether provider sign-in is needed, and adds an approval checkpoint before the
reservation is submitted. The finish line is a provider confirmation with the
restaurant, date, time, party size, and cancellation terms.

“Book me a ride to the airport tomorrow morning” works the same way. Genie asks
for the pickup point, destination, pickup time, ride preferences, and maximum
price. It checks for a direct provider tool first and browser automation second.
If sign-in is needed, Genie can prepare a domain-restricted Website Access
connection. You complete the sign-in on the provider page, then Genie can
continue with the approved session.

Passwords, passcodes, cookies, tokens, browser state, card numbers, and security
codes do not belong in Genie chat or a mission record. Enter them directly in
Website Access or the provider's secure checkout. Genie may ask whether a saved
payment method is available, but it does not ask for the payment details.

Before a booking, purchase, cancellation, message, or similar action, Genie
must show the exact provider, time, terms, and total cost and wait for approval.
After approval, it verifies the provider confirmation before reporting success.

The Missions page keeps the approach in a collapsed section so the main view
stays quiet. Open it to see missing details, secure setup, capability gaps,
planned steps, approval checkpoints, and the evidence Genie will use to decide
the work is complete. You can ask Genie to review the approach again after
installing a tool or completing Website Access.

## Ask for something

Two kinds of request land differently, and you do not have to say which:

- **A question** gets an answer. "What did I agree to in yesterday's notes?"
- **A request to set something up** gets built. "Send me a summary of overnight
  news every morning at seven."

The second is the interesting one. Genie hands it to the same builder that sits
behind Studio. It knows which skills, MCP servers, and delivery channels are
installed on your gateway, and it will not invent a tool you do
not have.

## It will ask you things

The builder asks for what it genuinely needs and nothing else. If you said
"every morning" without a time, you will be asked for a time; if you said
"email it to me" and no email channel is configured, you will be told that
before anything is built rather than after it silently delivers nothing.

Answer in the same conversation. Genie carries the build forward with your
answer.

## What you get

A real agent: saved, enabled, and visible in **Deployed** like anything you
would have built by hand. If you asked for something recurring, its schedule is
armed, and Genie says so.

You can ask Genie what it has built for you, and ask it to pause or cancel any
of it. Something you cannot find again is something you cannot stop, so
anything Genie builds is marked as Genie's and stays in reach of the
conversation that created it.

## When to use Studio instead

Genie builds agents. Studio edits them, and shows you what you have.

Reach for [Studio](studio.md) when you want to:

- change an agent that already exists;
- see or edit the YAML directly;
- build something with branching, loops, or custom code;
- approve an agent that would be reachable on a channel: that consent lives on
  screen, deliberately, and Genie cannot give it on your behalf.

## What Genie will not do

Genie is an operator, not an administrator. It cannot change gateway
configuration, restart the service, reach host credentials, or run shell
commands. If something needs one of those, it tells you which boundary it hit
instead of pretending the work is done.

It also never claims a result it has not seen. If a tool failed, you get the
failure.
