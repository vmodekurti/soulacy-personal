package docker

import "testing"

func TestNewDefaults(t *testing.T) {
	ex := New("", "", "")
	if ex.image != "python:3.12-slim" {
		t.Fatalf("image = %q", ex.image)
	}
	if ex.pythonBin != "python3" {
		t.Fatalf("pythonBin = %q", ex.pythonBin)
	}
	if ex.network != "none" {
		t.Fatalf("network = %q", ex.network)
	}
}

func TestNewCustom(t *testing.T) {
	ex := New("python:3.11", "python", "bridge")
	if ex.image != "python:3.11" || ex.pythonBin != "python" || ex.network != "bridge" {
		t.Fatalf("custom executor = %#v", ex)
	}
}
