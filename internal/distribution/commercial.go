package distribution

import "github.com/soulacy/soulacy/pkg/edition"

// This file is part of the combined repository only. During extraction it
// moves to soulacy-commercial; deleting it leaves a valid Personal registry.
func init() {
	for _, descriptor := range []edition.Descriptor{teamDescriptor(), scaleDescriptor()} {
		if err := Register(descriptor); err != nil {
			panic(err)
		}
	}
}

func teamDescriptor() edition.Descriptor {
	return edition.Descriptor{
		ID: edition.Team, DisplayName: "Teams", Commercial: true, Extends: edition.Personal,
		Capabilities: []edition.Capability{
			edition.MultiUser, edition.WorkspaceAdmin, edition.CentralizedProviders,
		},
	}
}

func scaleDescriptor() edition.Descriptor {
	return edition.Descriptor{
		ID: edition.Scale, DisplayName: "Scale", Commercial: true, Extends: edition.Team,
		Capabilities: []edition.Capability{
			edition.ExternalIdentity, edition.AuditReporting,
			edition.DistributedRuntime, edition.HighAvailability,
		},
	}
}
