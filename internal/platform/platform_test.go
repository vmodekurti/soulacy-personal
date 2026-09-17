package platform

import "testing"

func env(pairs map[string]string) func(string) string {
	return func(k string) string { return pairs[k] }
}

// Each platform is named from the variable it sets for its own tooling.
// Naming it matters: "you are on Railway" explains a missing shell in a way
// that "shell unavailable" never does.
func TestDetectNamesThePlatform(t *testing.T) {
	cases := []struct {
		vars map[string]string
		name string
		kind Kind
	}{
		{map[string]string{"RAILWAY_ENVIRONMENT": "production"}, "Railway", Managed},
		{map[string]string{"RAILWAY_PROJECT_ID": "abc"}, "Railway", Managed},
		{map[string]string{"FLY_APP_NAME": "soulacy"}, "Fly.io", Managed},
		{map[string]string{"RENDER": "true"}, "Render", Managed},
		{map[string]string{"K_SERVICE": "svc"}, "Google Cloud Run", Managed},
		{map[string]string{"DYNO": "web.1"}, "Heroku", Managed},
		{map[string]string{"KUBERNETES_SERVICE_HOST": "10.0.0.1"}, "Kubernetes", Container},
	}
	for _, c := range cases {
		got := DetectWith(env(c.vars), func() bool { return false })
		if got.Name != c.name || got.Kind != c.kind {
			t.Errorf("%v -> %+v, want %s/%s", c.vars, got, c.name, c.kind)
		}
	}
}

// Kubernetes and Docker are containers, not managed platforms: the operator
// owns the host and can reach a shell, so they keep the decisions a managed
// platform does not get.
func TestContainersAreNotManaged(t *testing.T) {
	if DetectWith(env(map[string]string{"KUBERNETES_SERVICE_HOST": "x"}), nil).IsManaged() {
		t.Error("Kubernetes is a container the operator runs, not a managed platform")
	}
	if DetectWith(env(nil), func() bool { return true }).IsManaged() {
		t.Error("Docker is a container the operator runs, not a managed platform")
	}
}

func TestBareMachineIsAHost(t *testing.T) {
	got := DetectWith(env(nil), func() bool { return false })
	if got.Kind != Host || got.IsManaged() {
		t.Errorf("got %+v, want a host", got)
	}
}

// A managed platform beats the Docker marker: everything on Railway is also a
// container, and the more specific answer is the useful one.
func TestManagedPlatformWinsOverTheDockerMarker(t *testing.T) {
	got := DetectWith(env(map[string]string{"FLY_APP_NAME": "soulacy"}), func() bool { return true })
	if got.Name != "Fly.io" {
		t.Errorf("got %+v, want Fly.io", got)
	}
}
