package registry

import (
	"context"
	"errors"
	"testing"

	"github.com/soulacy/soulacy/sdk/pkgregistry"
)

type stubPkgProvider struct{ id string }

func (s stubPkgProvider) ID() string { return s.id }
func (s stubPkgProvider) Search(context.Context, string) ([]pkgregistry.Package, error) {
	return nil, nil
}
func (s stubPkgProvider) Resolve(context.Context, string) (pkgregistry.Package, error) {
	return pkgregistry.Package{}, pkgregistry.ErrNotFound
}
func (s stubPkgProvider) Fetch(context.Context, pkgregistry.Package, string) error { return nil }

func TestPkgRegistry_RegisterAndNew(t *testing.T) {
	if err := RegisterPkgRegistry("test-pkg-http", func(cfg map[string]any) (pkgregistry.Provider, error) {
		id, _ := cfg["id"].(string)
		return stubPkgProvider{id: id}, nil
	}); err != nil {
		t.Fatalf("register: %v", err)
	}

	p, ok, err := NewPkgRegistry("test-pkg-http", map[string]any{"id": "main"})
	if !ok || err != nil {
		t.Fatalf("NewPkgRegistry: ok=%v err=%v", ok, err)
	}
	if p.ID() != "main" {
		t.Errorf("provider id = %q, want main", p.ID())
	}

	// Unknown type → ok=false, no error (host warns and skips).
	if _, ok, err := NewPkgRegistry("no-such-type", nil); ok || err != nil {
		t.Errorf("unknown type: ok=%v err=%v, want false/nil", ok, err)
	}

	// Duplicate registration errors.
	if err := RegisterPkgRegistry("test-pkg-http", func(map[string]any) (pkgregistry.Provider, error) {
		return stubPkgProvider{}, nil
	}); err == nil {
		t.Error("duplicate registration should error")
	}

	// Nil factory errors.
	if err := RegisterPkgRegistry("nil-factory", nil); err == nil {
		t.Error("nil factory should error")
	}

	// Factory errors propagate with ok=true.
	wantErr := errors.New("bad config")
	_ = RegisterPkgRegistry("test-pkg-err", func(map[string]any) (pkgregistry.Provider, error) {
		return nil, wantErr
	})
	if _, ok, err := NewPkgRegistry("test-pkg-err", nil); !ok || !errors.Is(err, wantErr) {
		t.Errorf("factory error: ok=%v err=%v", ok, err)
	}

	// Listing includes the registered names.
	names := PkgRegistries()
	found := false
	for _, n := range names {
		if n == "test-pkg-http" {
			found = true
		}
	}
	if !found {
		t.Errorf("PkgRegistries() = %v, missing test-pkg-http", names)
	}
}
