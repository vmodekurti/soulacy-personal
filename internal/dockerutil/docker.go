// Package dockerutil resolves the Docker CLI consistently for interactive
// shells and restricted service environments such as macOS launchd.
package dockerutil

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var macOSDockerCandidates = []string{
	"/usr/local/bin/docker",
	"/opt/homebrew/bin/docker",
	"/Applications/Docker.app/Contents/Resources/bin/docker",
}

var configuredRuntime struct {
	sync.RWMutex
	path string
}

// Configure supplies the deployment-pinned Docker CLI path loaded from
// config.yaml. Passing an empty value clears the override.
func Configure(path string) {
	configuredRuntime.Lock()
	configuredRuntime.path = strings.TrimSpace(path)
	configuredRuntime.Unlock()
}

// Resolve returns an absolute Docker CLI path. SOULACY_DOCKER_BIN is an
// operator override for non-standard installations. On macOS, well-known
// Docker Desktop paths are checked because launchd commonly has a minimal PATH.
func Resolve() (string, error) {
	configuredRuntime.RLock()
	configured := configuredRuntime.path
	configuredRuntime.RUnlock()
	if configured != "" {
		return resolveCandidate(configured)
	}
	if configured := strings.TrimSpace(os.Getenv("SOULACY_DOCKER_BIN")); configured != "" {
		return resolveCandidate(configured)
	}
	if found, err := exec.LookPath("docker"); err == nil {
		if absolute, absErr := filepath.Abs(found); absErr == nil {
			return absolute, nil
		}
		return found, nil
	}
	if runtime.GOOS == "darwin" {
		for _, candidate := range macOSDockerCandidates {
			if resolved, err := resolveCandidate(candidate); err == nil {
				return resolved, nil
			}
		}
	}
	return "", fmt.Errorf("Docker CLI not found; install Docker Desktop or set SOULACY_DOCKER_BIN to its absolute path")
}

func resolveCandidate(candidate string) (string, error) {
	resolved, err := exec.LookPath(candidate)
	if err != nil {
		return "", err
	}
	absolute, err := filepath.Abs(resolved)
	if err != nil {
		return "", err
	}
	return absolute, nil
}

// CommandContext creates a Docker command with a PATH that includes Docker
// Desktop's credential helpers. This environment is scoped to the Docker
// subprocess; Soulacy's gateway environment is not widened.
func CommandContext(ctx context.Context, args ...string) (*exec.Cmd, error) {
	bin, err := Resolve()
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = Environ()
	return cmd, nil
}

// Environ returns the current environment with only PATH augmented for Docker
// CLI helpers such as docker-credential-desktop.
func Environ() []string {
	env := os.Environ()
	pathValue := os.Getenv("PATH")
	if runtime.GOOS == "darwin" {
		for _, dir := range []string{"/Applications/Docker.app/Contents/Resources/bin", "/usr/local/bin", "/opt/homebrew/bin"} {
			if !pathListContains(pathValue, dir) {
				if pathValue == "" {
					pathValue = dir
				} else {
					pathValue += string(os.PathListSeparator) + dir
				}
			}
		}
	}
	for i, entry := range env {
		if strings.HasPrefix(entry, "PATH=") {
			env[i] = "PATH=" + pathValue
			return env
		}
	}
	return append(env, "PATH="+pathValue)
}

func pathListContains(value, candidate string) bool {
	for _, item := range filepath.SplitList(value) {
		if item == candidate {
			return true
		}
	}
	return false
}
