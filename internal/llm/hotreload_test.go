// hotreload_test.go — a deleted provider must stop being callable.
//
// Two of the three directions were already hot, which is what made the third
// dangerous rather than merely missing: saving a provider's credentials
// re-registered it live, so an operator had every reason to believe the router
// tracked the config. Deleting one removed it from the config file and left the
// constructed client in the router for the life of the process.
//
// Somebody removing a provider because its key had leaked would have seen it
// disappear from the settings page and kept serving requests with it.
package llm

import (
	"context"
	"testing"
)

type stubProvider struct{ id string }

func (s stubProvider) ID() string { return s.id }
func (s stubProvider) Complete(context.Context, CompletionRequest) (*CompletionResponse, error) {
	return &CompletionResponse{}, nil
}
func (s stubProvider) Models(context.Context) ([]string, error) { return nil, nil }

func TestUnregisterRemovesAProviderFromTheRouter(t *testing.T) {
	router := NewRouter("primary")
	router.Register(stubProvider{id: "primary"})
	router.Register(stubProvider{id: "leaked"})

	if !router.Unregister("leaked") {
		t.Fatal("Unregister reported nothing to remove")
	}
	if router.Provider("leaked") != nil {
		t.Error("a deleted provider is still resolvable, so every agent naming it keeps working")
	}
	if router.Provider("primary") == nil {
		t.Error("unregistering one provider removed another")
	}
}

func TestUnregisteringSomethingAbsentIsReportedNotAssumed(t *testing.T) {
	// The caller is a reconciliation loop that walks the router's own list, so
	// a miss means the two disagree about what is registered. Returning false
	// rather than silently succeeding is what lets the caller log the real
	// event instead of a routine one.
	router := NewRouter("primary")
	if router.Unregister("never-registered") {
		t.Error("Unregister claimed to remove a provider that was never registered")
	}
}

func TestTheDefaultProviderCanBeChangedAtRuntime(t *testing.T) {
	// defaultID was set at construction and only ever read, so editing
	// llm.default_provider updated everything that consults the config and
	// nothing that consults the router. The settings page agreed with the
	// operator while agents kept using the old provider, indefinitely and
	// silently.
	router := NewRouter("old")
	router.Register(stubProvider{id: "old"})
	router.Register(stubProvider{id: "new"})

	router.SetDefaultProvider("new")

	if router.DefaultProvider() != "new" {
		t.Errorf("DefaultProvider() = %q, want new", router.DefaultProvider())
	}
	// The resolution path, not just the accessor: an empty provider ID is what
	// an agent that names no provider sends, and that is the case the setting
	// exists for.
	if got := router.Provider(""); got == nil || got.ID() != "new" {
		t.Errorf("an unnamed provider resolved to %v, want new", got)
	}
}
