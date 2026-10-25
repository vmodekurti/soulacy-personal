package hooks

import (
	"context"
	"reflect"
	"testing"
)

func TestRegistryOrdersFiltersAndDenies(t *testing.T) {
	var seen []string
	r := &Registry{}
	if err := r.Register("context", HandlerFunc(func(_ context.Context, event Event) (Decision, error) {
		seen = append(seen, "context")
		return Decision{}, nil
	}), ContextAfter); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("guard", HandlerFunc(func(_ context.Context, event Event) (Decision, error) {
		seen = append(seen, "guard")
		return Decision{Deny: true, Reason: "blocked in test"}, nil
	}), ToolBefore); err != nil {
		t.Fatal(err)
	}
	decision, err := r.Run(context.Background(), Event{Stage: ToolBefore})
	if err != nil {
		t.Fatal(err)
	}
	if !decision.Deny || decision.Reason != "blocked in test" {
		t.Fatalf("decision = %+v", decision)
	}
	if !reflect.DeepEqual(seen, []string{"guard"}) {
		t.Fatalf("seen = %v", seen)
	}
}

func TestRegistryRejectsDuplicateNames(t *testing.T) {
	r := &Registry{}
	h := HandlerFunc(func(context.Context, Event) (Decision, error) { return Decision{}, nil })
	if err := r.Register("audit", h); err != nil {
		t.Fatal(err)
	}
	if err := r.Register("audit", h); err == nil {
		t.Fatal("expected duplicate registration error")
	}
}

func TestRegistryContainsHandlerPanics(t *testing.T) {
	r := &Registry{}
	_ = r.Register("broken", HandlerFunc(func(context.Context, Event) (Decision, error) {
		panic("boom")
	}))
	if _, err := r.Run(context.Background(), Event{Stage: RunBefore}); err == nil {
		t.Fatal("expected contained panic to become an error")
	}
}
