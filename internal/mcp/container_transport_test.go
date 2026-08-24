package mcp

import (
	"strings"
	"testing"
)

func TestContainerDockerArgsEnforcesApprovalBoundIsolation(t *testing.T) {
	digest := "ghcr.io/acme/server@sha256:" + strings.Repeat("a", 64)
	args, err := containerDockerArgs(ServerConfig{
		Command: digest, WorkDir: "/srv/soulacy/workspaces/ws-one",
		ContainerNetwork: "none", ContainerWorkspace: "read",
		ContainerDataDir: "/srv/soulacy/workspaces/ws-one/.mcp-data/server",
		Env:              map[string]string{"API_TOKEN": "workspace-token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, required := range []string{
		"--read-only", "--cap-drop ALL", "--security-opt no-new-privileges",
		"--network none", "--user ", "--pids-limit 128", "--memory 1g",
		"--memory-swap 1g", "--cpus 1", "--ulimit nofile=1024:1024",
		"src=/srv/soulacy/workspaces/ws-one,dst=/workspace,readonly",
		"type=bind,src=/srv/soulacy/workspaces/ws-one/.mcp-data/server,dst=/data",
	} {
		if !strings.Contains(joined, required) {
			t.Errorf("container args omit %q: %s", required, joined)
		}
	}
	for _, forbidden := range []string{"--privileged", "--network host", "/var/run/docker.sock"} {
		if strings.Contains(joined, forbidden) {
			t.Errorf("container args contain forbidden %q: %s", forbidden, joined)
		}
	}
}

func TestContainerDockerArgsRejectsUnapprovedPermissions(t *testing.T) {
	digest := "image@sha256:" + strings.Repeat("b", 64)
	for _, cfg := range []ServerConfig{
		{Command: digest, ContainerNetwork: "host"},
		{Command: digest, ContainerWorkspace: "all", WorkDir: "/tmp/ws"},
		{Command: digest, ContainerWorkspace: "read", WorkDir: "relative"},
	} {
		if _, err := containerDockerArgs(cfg); err == nil {
			t.Fatalf("unsafe container policy accepted: %+v", cfg)
		}
	}
}

func TestContainerDockerArgsAcceptsContentAddressedLocalImage(t *testing.T) {
	image := "sha256:" + strings.Repeat("a", 64)
	args, err := containerDockerArgs(ServerConfig{Command: image, ContainerNetwork: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if args[len(args)-1] != image {
		t.Fatalf("image argument = %q", args[len(args)-1])
	}
}
