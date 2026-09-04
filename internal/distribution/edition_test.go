package distribution

import (
	"testing"

	"github.com/soulacy/soulacy/pkg/edition"
)

func TestCommercialDistributionComposesEditionChain(t *testing.T) {
	team, err := Resolve(edition.Team)
	if err != nil || !team.Has(edition.MultiUser) {
		t.Fatalf("Teams is not composed: %#v, %v", team, err)
	}
	scale, err := Resolve(edition.Scale)
	if err != nil || scale.Extends != edition.Team || !scale.Has(edition.HighAvailability) {
		t.Fatalf("Scale is not composed over Teams: %#v, %v", scale, err)
	}
}
