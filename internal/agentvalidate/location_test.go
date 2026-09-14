package agentvalidate

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/agent"
)

func TestLocationTriggerValidation(t *testing.T) {
	good := &agent.Definition{ID: "office", Name: "Office", Trigger: agent.TriggerLocation, Enabled: true,
		Location: &agent.LocationTrigger{Name: "Office", Latitude: 37.77, Longitude: -122.41, RadiusM: 200, On: "enter", Cooldown: "30m"}}
	if r := Definition(good, "office.yaml", Options{}, Report{}); r.Errors > 0 {
		t.Fatalf("valid location trigger rejected: %+v", r.Findings)
	}
	bad := &agent.Definition{ID: "office", Name: "Office", Trigger: agent.TriggerLocation, Enabled: true,
		Location: &agent.LocationTrigger{Latitude: 95, Longitude: -122.41, RadiusM: 10, On: "near"}}
	r := Definition(bad, "office.yaml", Options{}, Report{})
	fields := map[string]bool{}
	for _, f := range r.Findings {
		fields[f.Field] = true
	}
	for _, want := range []string{"location.latitude", "location.radius_m", "location.on"} {
		if !fields[want] {
			t.Fatalf("expected an error on %s, got %+v", want, r.Findings)
		}
	}
	missing := &agent.Definition{ID: "office", Name: "Office", Trigger: agent.TriggerLocation, Enabled: true}
	if r := Definition(missing, "office.yaml", Options{}, Report{}); r.Errors == 0 {
		t.Fatal("location trigger without a region must be rejected")
	}
}
