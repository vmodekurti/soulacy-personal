package ssh

import "testing"

func TestNewBuildsTargetAndDefaultsPython(t *testing.T) {
	ex := New("box.example", "deploy", "", "")
	if ex.target != "deploy@box.example" {
		t.Fatalf("target = %q", ex.target)
	}
	if ex.pythonBin != "python3" {
		t.Fatalf("pythonBin = %q", ex.pythonBin)
	}
}

func TestNewKeepsExplicitUserInHost(t *testing.T) {
	ex := New("root@box.example", "deploy", "python", "/tmp/key")
	if ex.target != "root@box.example" {
		t.Fatalf("target = %q", ex.target)
	}
	if ex.pythonBin != "python" || ex.identityFile != "/tmp/key" {
		t.Fatalf("executor = %#v", ex)
	}
}
