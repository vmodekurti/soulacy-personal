package builder

import "testing"

func TestMCPRegistrySpecFromDetailSupportsRemoteOnlyServer(t *testing.T) {
	spec, err := mcpRegistrySpecFromDetail("ac.inference.sh/mcp", mcpRegDetailResponse{
		Server: &mcpRegServerDetail{
			Name:        "ac.inference.sh/mcp",
			Description: "remote only",
			Remotes: []mcpRegRemote{
				{Type: "streamable-http", URL: "https://sh.inference.ac"},
				{Type: "streamable-http", URL: "https://api.inference.sh/mcp"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Transport != "http" {
		t.Fatalf("transport = %q, want http", spec.Transport)
	}
	if spec.URL != "https://api.inference.sh/mcp" {
		t.Fatalf("url = %q, want /mcp endpoint", spec.URL)
	}
	if spec.Command != "" || len(spec.Args) != 0 {
		t.Fatalf("remote spec should not set stdio command/args: %+v", spec)
	}
	if len(spec.HeaderSchema) != 1 || spec.HeaderSchema[0].Name != "Authorization" || !spec.HeaderSchema[0].Required {
		t.Fatalf("expected inferred Authorization header, got %+v", spec.HeaderSchema)
	}
	if len(spec.SetupSteps) == 0 {
		t.Fatalf("expected guided setup steps")
	}
}

func TestMCPRegistrySpecFromDetailUnwrapsPackagedServer(t *testing.T) {
	spec, err := mcpRegistrySpecFromDetail("io.example/server", mcpRegDetailResponse{
		Server: &mcpRegServerDetail{
			Name: "io.example/server",
			Packages: []mcpRegPkg{
				{RegistryType: "npm", Identifier: "@example/server", RuntimeArguments: []string{"--stdio"}},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.Command != "npx" {
		t.Fatalf("command = %q, want npx", spec.Command)
	}
	if len(spec.Args) != 3 || spec.Args[0] != "-y" || spec.Args[1] != "@example/server" || spec.Args[2] != "--stdio" {
		t.Fatalf("unexpected args: %#v", spec.Args)
	}
	if len(spec.SetupSteps) == 0 {
		t.Fatalf("expected guided setup steps")
	}
}
