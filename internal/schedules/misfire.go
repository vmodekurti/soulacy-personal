package schedules

import (
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// misfire.go — MU-023 criterion 4: "misfire behavior is configurable and
// bounded; catch-up cannot create an unbounded burst."
//
// A gateway that was down has missed occurrences. What to do about them is a
// real choice with no universally right answer, so it is configurable — and
// both answers are wrong when taken to an extreme, so it is bounded.
//
//   - Replaying everything is not recovery. A gateway down for a week with a
//     */5 schedule missed two thousand occurrences; firing them all is a
//     self-inflicted denial of service, and it arrives as a burst precisely
//     when the system has just come back and is least able to absorb it.
//   - Replaying nothing loses work that mattered. A daily invoice run that
//     silently skipped because a deploy overlapped it is a real incident.
//
// So: skip by default, catch up on request, always capped, and always
// reporting how many were dropped. A silent cap is the worst of the three —
// it reads as "we caught up" when it means "we caught up a bit".

// Missed is the result of working out what a schedule owes after downtime.
type Missed struct {
	// Occurrences to replay, OLDEST FIRST.
	//
	// Order matters and newest-first is the tempting mistake: catch-up
	// replays a sequence of events, and a schedule that appends to something
	// would append them backwards. Oldest-first is also what makes the cap
	// meaningful — see Dropped.
	Occurrences []time.Time
	// Dropped is how many were discarded because the cap was reached.
	// Reported rather than swallowed: an operator who sees "caught up 3 runs"
	// and does not see "dropped 197" has been told something false.
	Dropped int
	// Policy is what was applied, echoed back so a caller logging the outcome
	// does not have to re-derive it.
	Policy string
}

// MissedSince computes the occurrences a schedule owes between two instants.
//
// `since` is normally the schedule's last completed fire; `now` is the current
// instant. Both exclusive of `since` and inclusive of `now`, so an occurrence
// is neither replayed twice nor skipped at a boundary.
func (s Schedule) MissedSince(parser cron.Parser, since, now time.Time) (Missed, error) {
	result := Missed{Policy: s.MisfirePolicy}
	if s.MisfirePolicy != MisfireCatchUp {
		return result, nil
	}
	if s.Cron == "" || since.IsZero() || !since.Before(now) {
		return result, nil
	}
	location, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return result, fmt.Errorf("schedules: timezone %q: %w", s.Timezone, err)
	}
	spec, err := parser.Parse(s.Cron)
	if err != nil {
		return result, fmt.Errorf("schedules: cron %q: %w", s.Cron, err)
	}

	limit := s.CatchUpLimit
	if limit <= 0 {
		limit = DefaultCatchUpLimit
	}

	// The walk itself is bounded independently of the cap. A malformed or
	// pathological expression yielding occurrences microseconds apart would
	// otherwise spin for as long as the downtime window is wide — the cap
	// bounds what we FIRE, this bounds what we ENUMERATE. Different failures,
	// different guards.
	const maxScan = 10000
	cursor := since.In(location)
	deadline := now.In(location)
	var all []time.Time
	for scanned := 0; scanned < maxScan; scanned++ {
		next := spec.Next(cursor)
		if next.IsZero() || next.After(deadline) {
			break
		}
		if !next.After(cursor) {
			// A spec that does not advance would loop forever. Refusing beats
			// spinning: the schedule is broken, and saying so is more useful
			// than quietly producing nothing.
			return result, fmt.Errorf("schedules: cron %q does not advance", s.Cron)
		}
		all = append(all, next.UTC())
		cursor = next
	}

	// Over the cap, keep the NEWEST. Replaying the oldest N and dropping the
	// rest delivers the most stale work and discards the most current — the
	// opposite of what somebody asking for catch-up wants. Oldest-first
	// within the kept window, because catch-up replays a sequence.
	if len(all) > limit {
		result.Dropped = len(all) - limit
		all = all[len(all)-limit:]
	}
	result.Occurrences = all
	return result, nil
}

// NextFire returns the next instant a schedule is due after `after`.
func (s Schedule) NextFire(parser cron.Parser, after time.Time) (time.Time, error) {
	if s.Cron == "" {
		return time.Time{}, nil
	}
	location, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedules: timezone %q: %w", s.Timezone, err)
	}
	spec, err := parser.Parse(s.Cron)
	if err != nil {
		return time.Time{}, fmt.Errorf("schedules: cron %q: %w", s.Cron, err)
	}
	return spec.Next(after.In(location)).UTC(), nil
}
