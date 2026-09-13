# Your first successful run

**Outcome:** open your workspace, connect one model, and turn supplied notes
into a checkable answer. No email, write tools, or schedule is needed.

## Before you start

You need a Mac or Linux machine for the gateway, or an existing hosted gateway
and its login credential. You also need either a running local provider with a
downloaded model, or access to a cloud model. Cloud inference may cost money.

If someone has given you a gateway URL and credential, **skip to step 3**.
Do not install a second server just to connect a browser or phone.

## 1. Install the gateway and CLI

=== "Installer (macOS / Linux)"

    ```bash
    curl -fsSL https://soulacy.io/install.sh | bash
    sy version
    ```

    This executes the project's installer. If your organization requires script
    review, download and inspect it first. Follow any PATH instruction it prints,
    then open a new terminal if `sy` is not found.

=== "Build from source"

    Install the [prerequisites](installation.md), including the supported Go
    toolchain, Node.js, and platform SQLite/C compiler requirements.

    ```bash
    git clone https://github.com/vmodekurti/soulacy-personal.git soulacy
    cd soulacy
    make all
    ./bin/sy version
    ```

    Below, use `./bin/sy` and `./bin/soulacy` from this directory instead of
    `sy` and `soulacy` unless you install the binaries on PATH.

**Checkpoint:** `sy version` prints a version. If not, fix installation first.
[Other options](installation.md) include Docker and [cloud hosting](../deployment/cloud.md).

## 2. Set up one provider and start the gateway

```bash
sy onboard
```

Keep the local-only bind address for this exercise. Choose a provider and a
model you actually have access to. A **gateway API key** signs you into Soulacy;
a **provider API key** authenticates with the model service. Store both privately.

`sy onboard` updates an existing workspace without replacing unrelated settings.
`sy setup` is the separate, fresh-config path—do not use it casually to repair
an existing installation.

If you did not start a background service during onboarding:

```bash
soulacy
```

Leave that terminal open while testing. If a service already owns the port,
use it; do not launch a competing gateway.

**Checkpoint:** the server reports its address, normally `http://localhost:18789`.
A port-in-use error is not a reason to delete your workspace.
See [first checks](../troubleshooting/first-checks.md).

## 3. Sign in and choose the model

1. Open the gateway URL. For a local installation, use
   `http://localhost:18789` on that same computer.
2. Sign in with the gateway credential from onboarding or your server owner.
   Cloud owners can find it using the [cloud login instructions](../deployment/cloud.md).
3. Open **Providers**, check your provider, and test its connection. Choose a
   model the provider actually lists.
4. Open **Agents**. Select a suitable starter agent or choose **+ New Agent**.
   Give a new agent a unique ID such as `notes-assistant`, choose your
   provider/model, enable HTTP chat, and save it.

Use these instructions for this notes-only agent:

```text
Turn the user's supplied notes into an action plan.
Use only those notes. Do not browse, send messages, or change files.
List each action and its owner. If an owner or date is absent, say "Not specified".
Finish with a section called "Open questions". Never invent a commitment.
```

Set built-in tools to **None**, and leave external tools, skills, and MCP
integrations unconfigured. YAML users can start with the
[downloadable definition](../examples/notes-assistant/SOUL.yaml); replace its
model/provider placeholders before using it.

**Checkpoint:** the saved agent is enabled. Inspect its **Model preparation**
panel for the saved provider/model and any blocking reason. “Unknown” metadata
is not a failed connection. [Understand preparation](../using/model-preparation.md).

## 4. Send a request you can verify without the internet

Open **Chat**, select the agent, and paste:

```text
Create an action plan from these fictional project notes:
- Maya will send the draft on Tuesday.
- Leo will review it after the draft arrives.
- Someone needs to arrange the customer demo; no date is agreed.
```

**A good answer has:** Maya → draft → Tuesday; Leo → review → after draft;
demo → owner/date not specified; and Open questions asking who owns the demo
and when it should happen. Wording can vary. It must not invent a calendar
invitation, an external send receipt, or a commitment.

Open **Activity** and inspect the completed run. Expect a model call and no
external tool write. A technically successful response can still be wrong:
adjust the instructions and test again if it invents details.

## 5. Check the unhappy path before automating

Send `There are no project notes yet. What are the next actions?` It should ask
for notes or acknowledge the gap, not manufacture tasks. Continue with the
[full notes exercise](../use-cases/notes-to-action-plan.md) for contradictory
notes and a request to send email.

For a local installation, run `sy doctor` using the same workspace, user, and
environment as the gateway service. Doctor checks configuration/reachability;
it does not guarantee output quality.

## You are ready when…

- You can sign in, select the intended agent, and receive a reply.
- The reply uses supplied evidence and admits missing information.
- Activity shows the intended provider/model and no unexpected tools.
- You know which computer must stay on for the gateway to run.

Next: [connect your iPhone](iphone.md), [teach a preference](../use-cases/teach-a-preference.md),
or [search your documents](../use-cases/handbook-answers.md). Add one capability
at a time so a failure has an identifiable cause.
