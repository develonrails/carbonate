package main

import (
	"testing"
)

func TestResolvePathPrefersTheOverride(t *testing.T) {
	got, err := resolvePath("/tmp/explicit.json")
	if err != nil {
		t.Fatalf("resolvePath: %v", err)
	}

	if got != "/tmp/explicit.json" {
		t.Errorf("resolvePath = %q, want the override", got)
	}
}

func TestBridgePasswordFromEnvironment(t *testing.T) {
	t.Setenv("CARBONATE_BRIDGE_PASSWORD", "from-env")

	got, err := bridgePassword()
	if err != nil {
		t.Fatalf("bridgePassword: %v", err)
	}

	if got != "from-env" {
		t.Errorf("bridgePassword = %q, want %q", got, "from-env")
	}
}
