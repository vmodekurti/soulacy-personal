// Package platform names where Soulacy is running.
//
// It exists because two very different questions have the same answer. A user
// needs to be told why their deployment cannot do something ("you are on
// Railway, there is no shell here"), and the gateway needs to decide what to
// permit — a managed platform is one where the operator cannot reach a shell
// to undo a mistake, and where the gateway is usually reachable from the
// internet.
//
// Detection reads the environment variables platforms set for their own
// tooling. It is a hint, not proof, so it is used to *withhold* privilege and
// to explain, never to grant anything.
package platform

import (
	"os"
	"runtime"
)

// Kind is how much the deployment can be asked to do.
type Kind string

const (
	// Managed is a platform-as-a-service: no shell the user can reach, no
	// root, and a filesystem that resets on deploy except for a volume.
	Managed Kind = "paas"
	// Container is a container the operator runs themselves — they own the
	// host and can exec into it.
	Container Kind = "container"
	// Host is an ordinary machine.
	Host Kind = "host"
)

// Info is the detected platform.
type Info struct {
	Name string
	Kind Kind
}

// IsManaged reports a platform where the user has no shell of their own.
func (i Info) IsManaged() bool { return i.Kind == Managed }

// Detect names the platform from the current environment.
func Detect() Info { return DetectWith(os.Getenv, dockerEnvExists) }

// DetectWith is Detect with its two inputs injected, so a test can describe a
// deployment instead of describing the machine running the test.
func DetectWith(env func(string) string, inDocker func() bool) Info {
	switch {
	case env("RAILWAY_ENVIRONMENT") != "" || env("RAILWAY_PROJECT_ID") != "":
		return Info{"Railway", Managed}
	case env("FLY_APP_NAME") != "":
		return Info{"Fly.io", Managed}
	case env("RENDER") != "" || env("RENDER_SERVICE_ID") != "":
		return Info{"Render", Managed}
	case env("K_SERVICE") != "":
		return Info{"Google Cloud Run", Managed}
	case env("DYNO") != "":
		return Info{"Heroku", Managed}
	case env("KUBERNETES_SERVICE_HOST") != "":
		return Info{"Kubernetes", Container}
	case inDocker != nil && inDocker():
		return Info{"Docker", Container}
	}
	return Info{"self-hosted (" + runtime.GOOS + ")", Host}
}

func dockerEnvExists() bool {
	_, err := os.Stat("/.dockerenv")
	return err == nil
}
