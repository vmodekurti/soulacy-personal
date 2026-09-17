package config

import (
	"strings"
	"testing"

	"github.com/soulacy/soulacy/internal/platform"
)

func withGrant(ids ...string) *Config {
	c := &Config{}
	c.Runtime.AllowSystemAgents = ids
	return c
}

// The rule: a managed platform does not get the shell grant, whatever the
// config file says. The operator there has no shell of their own to undo what
// an agent does, and the gateway is usually reachable from the internet.
func TestShellGrantIsWithheldOnManagedPlatforms(t *testing.T) {
	for _, p := range []platform.Info{
		{Name: "Railway", Kind: platform.Managed},
		{Name: "Fly.io", Kind: platform.Managed},
		{Name: "Heroku", Kind: platform.Managed},
	} {
		cfg := withGrant("system", "ops")
		applyManagedPlatformPolicy(cfg, p)
		if len(cfg.Runtime.AllowSystemAgents) != 0 {
			t.Errorf("%s: grant survived: %v", p.Name, cfg.Runtime.AllowSystemAgents)
		}
		if ShellGrantWithheldReason == "" {
			t.Errorf("%s: withheld with no reason — the operator set this and needs to know why it did nothing", p.Name)
		}
		if !strings.Contains(ShellGrantWithheldReason, p.Name) {
			t.Errorf("%s: the reason should name the platform: %q", p.Name, ShellGrantWithheldReason)
		}
		// The reason has to carry the way that does work, or it is only a refusal.
		if !strings.Contains(ShellGrantWithheldReason, "package_install") {
			t.Errorf("%s: the reason should point at what still works: %q", p.Name, ShellGrantWithheldReason)
		}
		// It should say which agents lost the grant, so the operator can see
		// their own configuration reflected back.
		if !strings.Contains(ShellGrantWithheldReason, "system, ops") {
			t.Errorf("%s: the reason should quote what was withheld: %q", p.Name, ShellGrantWithheldReason)
		}
	}
}

// A machine the operator owns keeps the decision. This is the line between
// "we protect you from a platform you cannot repair" and "we decide for you".
func TestShellGrantSurvivesWhereYouOwnTheHost(t *testing.T) {
	for _, p := range []platform.Info{
		{Name: "Docker", Kind: platform.Container},
		{Name: "Kubernetes", Kind: platform.Container},
		{Name: "self-hosted (linux)", Kind: platform.Host},
	} {
		cfg := withGrant("system")
		applyManagedPlatformPolicy(cfg, p)
		if len(cfg.Runtime.AllowSystemAgents) != 1 {
			t.Errorf("%s: the grant should stand where the operator owns the host", p.Name)
		}
		if ShellGrantWithheldReason != "" {
			t.Errorf("%s: nothing was withheld, so there is nothing to explain: %q", p.Name, ShellGrantWithheldReason)
		}
	}
}

// Nothing configured is not something withheld. Reporting a reason here would
// tell every PaaS user their grant was refused when they never set one.
func TestNoGrantMeansNothingToWithhold(t *testing.T) {
	cfg := &Config{}
	applyManagedPlatformPolicy(cfg, platform.Info{Name: "Railway", Kind: platform.Managed})
	if ShellGrantWithheldReason != "" {
		t.Errorf("no grant was set, so nothing was withheld: %q", ShellGrantWithheldReason)
	}
}

// The reason is global state read by the doctor endpoint; a later clean load
// must clear it rather than leaving a stale explanation on screen.
func TestTheReasonDoesNotOutliveTheConditionThatSetIt(t *testing.T) {
	applyManagedPlatformPolicy(withGrant("system"), platform.Info{Name: "Fly.io", Kind: platform.Managed})
	if ShellGrantWithheldReason == "" {
		t.Fatal("premise: a reason was set")
	}
	applyManagedPlatformPolicy(withGrant("system"), platform.Info{Name: "Docker", Kind: platform.Container})
	if ShellGrantWithheldReason != "" {
		t.Errorf("a stale reason survived onto a deployment that withheld nothing: %q", ShellGrantWithheldReason)
	}
}

func TestNilConfigIsNotAPanic(t *testing.T) {
	applyManagedPlatformPolicy(nil, platform.Info{Name: "Railway", Kind: platform.Managed})
}
