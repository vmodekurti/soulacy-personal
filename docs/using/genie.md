# Talking to Genie

Genie is the way in. You ask for something in your own words, and Genie either
answers it or builds the thing that will keep answering it.

Most of Soulacy — Studio, the agent list, schedules, delivery channels — is
still there, and you will want it eventually. You do not need any of it to
start.

## Ask for something

Two kinds of request land differently, and you do not have to say which:

- **A question** gets an answer. "What did I agree to in yesterday's notes?"
- **A request to set something up** gets built. "Send me a summary of overnight
  news every morning at seven."

The second is the interesting one. Genie hands it to the same builder that sits
behind Studio, which knows what is installed on your gateway — which skills,
which MCP servers, which delivery channels — and will not invent a tool you do
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
- approve an agent that would be reachable on a channel — that consent lives on
  screen, deliberately, and Genie cannot give it on your behalf.

## What Genie will not do

Genie is an operator, not an administrator. It cannot change gateway
configuration, restart the service, reach host credentials, or run shell
commands. If something needs one of those, it tells you which boundary it hit
instead of pretending the work is done.

It also never claims a result it has not seen. If a tool failed, you get the
failure.
