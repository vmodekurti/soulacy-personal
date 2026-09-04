package tour

// pages.go — each screen's part in the one story.
//
// The wording is deliberately about consequence rather than controls. "Delivery
// is where you configure channel adapters" tells you nothing you could not read
// off the page. "Until something is set up here, your agent's work has nowhere
// to go — it runs, it finishes, and you never hear about it" tells you why you
// are standing here.

import "fmt"

var pages = map[string]page{
	"providers": {
		stage: StageBrain, nextAction: "open_providers", nextLabel: "Add a provider",
		role:         "This is where an agent gets something to think with.",
		contribution: "Nothing else on this list works until one model is reachable — an agent with no model is a plan nobody can read.",
		whenEmpty: func(InstallState) string {
			return "There is no model connected yet, so this is genuinely the first stop. Add one provider — local or hosted — and every other screen starts working."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("You have %s connected. Worth returning here when answers get worse for no obvious reason: a provider that is rate-limiting or has lost its key fails quietly, and this is where that shows.",
				plural(s.Providers, "provider", "providers"))
		},
	},

	"studio": {
		stage: StagePlan, nextAction: "open_studio", nextLabel: "Open Studio",
		role:         "This is where the agent is actually built.",
		contribution: "You describe the job in plain language; Studio drafts the steps, checks them, and hands you something that runs.",
		whenEmpty: func(InstallState) string {
			return "You have not built anything yet. Describe the job the way you would to a colleague — what should happen, what it should produce, who it should tell — and let it draft the first version. It is easier to correct a draft than to start from an empty canvas."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("You have %s built. Everything else you will see is either fuel for this screen or a view of what it produced.",
				plural(s.Agents, "agent", "agents"))
		},
	},

	"agents": {
		stage: StagePlan, nextAction: "", nextLabel: "",
		role:         "Everything you have built, and whether it is switched on.",
		contribution: "Studio saves agents switched off on purpose — this is where you decide something is ready to be real.",
		whenEmpty: func(InstallState) string {
			return "Nothing deployed yet. This page fills up once you save from Studio; an agent sits here disabled until you turn it on, so nothing you are experimenting with can run behind your back."
		},
		whenUsed: func(s InstallState) string {
			if s.EnabledAgents == 0 {
				return fmt.Sprintf("You have %s here and none of them enabled — which means nothing is running. Turning one on is what makes it real.",
					plural(s.Agents, "agent", "agents"))
			}
			return fmt.Sprintf("%s here, %d switched on. This is also where you retire one: disabling beats deleting when you are not sure.",
				plural(s.Agents, "agent", "agents"), s.EnabledAgents)
		},
	},

	"channels": {
		stage: StageMouth, nextAction: "open_delivery", nextLabel: "Set up a destination",
		role:         "Where an agent's work comes out.",
		contribution: "This is the difference between an agent that works and an agent you benefit from.",
		whenEmpty: func(InstallState) string {
			return "Nothing is set up here, so your agents have nowhere to put their results. They will run, finish, and you will never hear about it — the work lands in Runs and stays there. Connect one destination you actually read: Telegram, Slack, email."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s configured. Worth knowing: HTTP is how requests come IN, not where results go OUT — an agent whose only channel is HTTP still has nowhere to deliver.",
				plural(s.DeliveryChannels, "destination", "destinations"))
		},
	},

	"schedule": {
		stage: StageClock, nextAction: "", nextLabel: "",
		role:         "When it happens without you.",
		contribution: "An agent you have to trigger is a tool. An agent that runs on its own is the point.",
		whenEmpty: func(InstallState) string {
			return "Nothing is scheduled. Once an agent does the right thing when you press the button, come here and stop pressing the button — that is the whole return on having built it."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s running on a clock. One thing worth checking: a scheduled run has nobody present to approve a confirmation prompt, so an agent that asks before privileged actions will fail rather than wait.",
				plural(s.Schedules, "automation", "automations"))
		},
	},

	"activity": {
		stage: StageEyes, nextAction: "", nextLabel: "",
		role:         "What actually happened, run by run.",
		contribution: "The gap between 'it should work' and 'it worked' is closed here, and nowhere else.",
		whenEmpty: func(InstallState) string {
			return "Nothing has run yet. When it has, this is the first place to look: every run keeps its full action log, what it cost, and a plain-language reading of why it failed."
		},
		whenUsed: func(s InstallState) string {
			base := fmt.Sprintf("%s recorded.", plural(s.Runs, "run", "runs"))
			if s.FailedRuns > 0 {
				return base + fmt.Sprintf(" %s failed recently — a failed run can be sent straight back into Studio from here, with the failure attached, rather than reproduced by hand.",
					plural(s.FailedRuns, "run", "runs"))
			}
			return base + " Nothing failing. This is also where cost per run shows up, which is usually more interesting than it sounds."
		},
	},

	"dashboard": {
		stage: StageEyes, nextAction: "", nextLabel: "",
		role:         "The overnight summary.",
		contribution: "If something broke while you were away, it surfaces here before anywhere else.",
		whenEmpty: func(InstallState) string {
			return "Quiet so far, because nothing has run. Once agents are working this becomes the screen you glance at rather than the one you study — health, failure rate, and what needs attention."
		},
		whenUsed: func(s InstallState) string {
			return "Glance here first thing. Failure rate and recent errors are on the left; the launch-readiness score at the bottom is an honest opinion about whether this install is fit to be relied on."
		},
	},

	"knowledge": {
		stage: StageMaterial, nextAction: "", nextLabel: "",
		role:         "Facts that are yours, not the model's.",
		contribution: "It is what stops an agent guessing at things only your documents know.",
		whenEmpty: func(InstallState) string {
			return "Nothing here yet, and you may never need it. Add a knowledge base at the point where you catch an agent inventing something your own documents could have told it — not before."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s available. An agent can search and cite these rather than improvising, which is the difference between a plausible answer and a correct one.",
				plural(s.KnowledgeBases, "knowledge base", "knowledge bases"))
		},
	},

	"skills": {
		stage: StageMaterial, nextAction: "", nextLabel: "",
		role:         "Know-how an agent can load, rather than know-how you retype.",
		contribution: "When several agents need the same expertise, it belongs here instead of in each prompt.",
		whenEmpty: func(InstallState) string {
			return "Nothing installed. The moment to come here is the second time you find yourself pasting the same paragraph of instructions into a different agent."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s installed. An agent loads one when the task calls for it, so the instructions stay in one place and improve for everybody at once.",
				plural(s.Skills, "skill", "skills"))
		},
	},

	"mcp": {
		stage: StageMaterial, nextAction: "open_mcp", nextLabel: "Connect a server",
		role:         "How an agent reaches the world outside this box.",
		contribution: "Built-in tools cover the basics; everything specific to your life arrives through here.",
		whenEmpty: func(InstallState) string {
			return "No servers connected. This is the answer whenever you find yourself thinking 'it would need access to X' — connect X here and it shows up in Studio's palette as something to drag onto the canvas."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s connected; their tools appear in Studio's palette automatically. Worth remembering when a workflow feels like it needs custom code — often a tool already does it, with argument names that are known to be right.",
				plural(s.MCPServers, "server", "servers"))
		},
	},

	"chat": {
		stage: StagePlan, nextAction: "", nextLabel: "",
		role:         "A direct line to any agent you have built.",
		contribution: "The fastest way to find out whether a change actually improved anything.",
		whenEmpty: func(InstallState) string {
			return "Nothing to talk to yet. Once you have built something, this is where you check it behaves before you let it loose on a schedule."
		},
		whenUsed: func(InstallState) string {
			return "Talk to any agent here with the full trace of each turn beside it — what it called, what came back, what it cost. Faster than a real run and it tells you more."
		},
	},

	"templates": {
		stage: StagePlan, nextAction: "", nextLabel: "",
		role:         "Somebody else's working agent, as a starting point.",
		contribution: "Most useful when you know roughly what you want but not how to phrase it.",
		whenEmpty: func(InstallState) string {
			return "Deploy one in a click and then open it in Studio to see how it was put together. Reading a working example is usually faster than describing a new one."
		},
		whenUsed: func(InstallState) string {
			return "Deploy one in a click, then open it in Studio and change it. Nothing here is precious — a template is a first draft with the awkward parts already solved."
		},
	},

	"memory": {
		stage: StageGrowth, nextAction: "", nextLabel: "",
		role:         "What your agents have picked up along the way.",
		contribution: "It is how an agent stops making the same mistake, and how you notice when it has learned the wrong lesson.",
		whenEmpty: func(InstallState) string {
			return "Nothing learned yet — this fills as agents run. The review queue is the part to care about: it is where you approve what an agent wants to remember, before it acts on it."
		},
		whenUsed: func(s InstallState) string {
			if s.LearningPending > 0 {
				return fmt.Sprintf("%s waiting for your review. Worth clearing: until you do, those are things an agent has decided it knows without anyone checking.",
					plural(s.LearningPending, "item", "items"))
			}
			return "Nothing pending. Come here when an agent starts behaving in a way you did not ask for — usually it has remembered something it should not have."
		},
	},

	"queues": {
		stage: StageMaterial, nextAction: "", nextLabel: "",
		role:         "Where work waits between the steps of a workflow.",
		contribution: "Almost always something you can ignore, until a multi-step agent stalls halfway.",
		whenEmpty: func(InstallState) string {
			return "Nothing here, and that is normal. This is a diagnostic view: you come looking when a workflow that should have finished appears to have stopped between two steps."
		},
		whenUsed: func(InstallState) string {
			return "Buffers carrying work between steps and between agents. Only interesting when something is stuck — a queue that is not draining tells you which step is the problem."
		},
	},

	"workboard": {
		stage: StageGrowth, nextAction: "", nextLabel: "",
		role:         "Work your agents have set aside — for themselves, or for you.",
		contribution: "An agent that hits something it cannot finish leaves it here rather than dropping it.",
		whenEmpty: func(InstallState) string {
			return "Empty. Items appear when an agent decides something needs doing later, or needs a human — so an empty board means nothing has been handed back to you."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s on the board. Anything addressed to you is a thing an agent could not finish alone, which is usually worth reading before it goes stale.",
				plural(s.OpenTasks, "item", "items"))
		},
	},

	"logs": {
		stage: StageEyes, nextAction: "", nextLabel: "",
		role:         "The raw gateway log, live.",
		contribution: "The last resort, for when a failure has no readable explanation anywhere else.",
		whenEmpty: func(InstallState) string {
			return "Try Runs before here. Runs explains failures in plain language; this is the unfiltered version for when that explanation is not enough."
		},
		whenUsed: func(InstallState) string {
			return "Try Runs first — it explains failures in plain language and links them to the step that broke. Come here when that is not enough, or when something failed before a run even started."
		},
	},

	"browser": {
		stage: StageEyes, nextAction: "", nextLabel: "",
		role:         "A replay of what a browsing agent saw and clicked.",
		contribution: "The only way to find out why a web-browsing agent came back with the wrong answer.",
		whenEmpty: func(InstallState) string {
			return "Nothing to replay yet. When an agent that browses returns something odd, the answer is almost always visible here — usually it landed somewhere you did not expect."
		},
		whenUsed: func(InstallState) string {
			return "Step through what the agent actually saw. When a browsing agent is wrong, it is rarely reasoning badly about the right page — it is usually reasoning fine about the wrong one."
		},
	},

	"mobile": {
		stage: StageEyes, nextAction: "", nextLabel: "",
		role:         "The phone-sized version of all this.",
		contribution: "It is what lets an agent ask you something while you are away from the desk.",
		whenEmpty: func(InstallState) string {
			return "Pin this on your phone. An agent that needs approval before doing something privileged will wait for you here — otherwise it waits until you are back at a laptop, or gives up."
		},
		whenUsed: func(InstallState) string {
			return "Approvals waiting on you, and quick run controls. Worth pinning on a phone: it is the difference between an agent that pauses politely and one that fails because nobody was around."
		},
	},

	"secrets": {
		stage: StageKeeping, nextAction: "open_secrets", nextLabel: "Store a secret",
		role:         "Credentials, kept out of your configuration files.",
		contribution: "It means a key lives in one encrypted place and is referred to by name everywhere else.",
		whenEmpty: func(InstallState) string {
			return "Nothing stored. Use this the first time a setup screen asks for a token: store it here, refer to it by name, and it never ends up sitting in a YAML file you might share."
		},
		whenUsed: func(InstallState) string {
			return "Stored keys, referenced by name. Rotating one here changes it everywhere it is used, which is the reason to bother."
		},
	},

	"config": {
		stage: StageKeeping, nextAction: "", nextLabel: "",
		role:         "The settings with no screen of their own.",
		contribution: "Also the honest view of exactly what is on disk.",
		whenEmpty: func(InstallState) string {
			return "Most things have a dedicated screen; this is for the rest — and for seeing precisely what the gateway is running with when a setting is not behaving as you expect."
		},
		whenUsed: func(InstallState) string {
			return "Everything the gateway is configured with. Most of it is reachable from a friendlier screen; come here when you want to see the actual values rather than a form's idea of them."
		},
	},

	"pluginmgr": {
		stage: StageKeeping, nextAction: "", nextLabel: "",
		role:         "Bundles that add capabilities the core does not have.",
		contribution: "A plugin can bring tools, destinations, even whole pages of this interface.",
		whenEmpty: func(InstallState) string {
			return "None installed. Worth a look when the thing you need is not built in — it is often already packaged rather than needing to be built."
		},
		whenUsed: func(s InstallState) string {
			return fmt.Sprintf("%s installed. They can add tools, destinations, and pages, so if this interface has a screen you do not recognise, it probably arrived from here.",
				plural(s.Plugins, "plugin", "plugins"))
		},
	},

	"onboarding": {
		stage: StageKeeping, nextAction: "", nextLabel: "",
		role:         "The setup checklist, re-checked every time you open it.",
		contribution: "It is the shortest honest answer to 'is this install actually ready'.",
		whenEmpty: func(InstallState) string {
			return "It reads live state rather than remembering what you clicked, so it stays useful long after the first day. If something on it is red, that is the thing standing between you and a working agent."
		},
		whenUsed: func(InstallState) string {
			return "Re-checks itself every time, so it never claims a step is done because you once clicked it. Worth a look after any change to providers or channels."
		},
	},
}
