package tour

// state.go — what the platform knows about this install.
//
// The tour is adaptive: it should never explain how to connect a model to
// someone who connected one last week, and it should never open with "here is
// what Studio does" when the reason nothing works is that there is no delivery
// channel. So every page's narrative is written against this snapshot rather
// than as fixed copy.
//
// Deliberately a plain struct of counts. The gateway fills it from the same
// sources the Dashboard and readiness already use; this package stays pure and
// testable, with no HTTP, no loader, and no clock.

// InstallState is a snapshot of what exists right now.
type InstallState struct {
	Providers        int // LLM providers the router actually registered
	Agents           int // saved agents, enabled or not
	EnabledAgents    int
	DeliveryChannels int // configured channels that can carry a result OUT
	Schedules        int // agents with a cron trigger
	Runs             int // runs recorded, ever
	FailedRuns       int // runs that failed in the recent window
	KnowledgeBases   int
	Skills           int
	MCPServers       int
	Plugins          int
	LearningPending  int // memories/procedures waiting for review
	OpenTasks        int // workboard items
}

// Stage is one link in the chain between "nothing" and the outcome.
//
// The order is the order of dependency, not the order of the sidebar: an agent
// with no brain cannot run, an agent with no mouth cannot reach you, and an
// agent with no clock only runs when you are watching — which is the opposite
// of the point.
type Stage int

const (
	StageBrain    Stage = iota // a model to think with
	StagePlan                  // an agent that knows what to do
	StageMaterial              // what it knows and what it can touch
	StageMouth                 // where the result comes out
	StageClock                 // when it happens without you
	StageEyes                  // how you know it worked
	StageGrowth                // how it gets better
	StageKeeping               // the plumbing that keeps it honest
)

func (s Stage) String() string {
	switch s {
	case StageBrain:
		return "brain"
	case StagePlan:
		return "plan"
	case StageMaterial:
		return "materials"
	case StageMouth:
		return "voice"
	case StageClock:
		return "clock"
	case StageEyes:
		return "eyes"
	case StageGrowth:
		return "growth"
	default:
		return "keeping"
	}
}

// Met reports whether this link in the chain is in place.
func (s Stage) Met(st InstallState) bool {
	switch s {
	case StageBrain:
		return st.Providers > 0
	case StagePlan:
		return st.Agents > 0
	case StageMaterial:
		return st.KnowledgeBases+st.Skills+st.MCPServers > 0
	case StageMouth:
		return st.DeliveryChannels > 0
	case StageClock:
		return st.Schedules > 0
	case StageEyes:
		return st.Runs > 0
	case StageGrowth:
		return st.Runs > 0
	default:
		return true
	}
}

// FirstUnmet returns the earliest link in the chain that is missing, and true.
// When the whole chain is in place it returns false — the story then becomes
// about improving what runs rather than getting it to run at all.
//
// StageGrowth and StageKeeping are excluded: they are never blocking, and
// reporting "you have not reviewed any learning yet" as the thing standing
// between you and a working agent would be a lie.
func FirstUnmet(st InstallState) (Stage, bool) {
	for _, s := range []Stage{StageBrain, StagePlan, StageMaterial, StageMouth, StageClock, StageEyes} {
		if !s.Met(st) {
			return s, true
		}
	}
	return StageKeeping, false
}
