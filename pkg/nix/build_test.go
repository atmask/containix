package nix

import (
	"os/exec"
	"strings"
	"testing"
)

// requireNix skips the test if `nix` isn't on PATH. These tests integrate
// with a real Nix install — they're slow and depend on network + the local
// Nix store. Use `go test -short` to skip them outright during fast cycles.
func requireNix(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping integration test in -short mode")
	}
	if _, err := exec.LookPath("nix"); err != nil {
		t.Skip("nix not available on PATH; skipping integration test")
	}
}

func TestBuild_RealFlakeRef(t *testing.T) {
	requireNix(t)

	// nixpkgs#hello is the canonical smoke-test target: tiny closure (~5
	// paths), available on cache.nixos.org, no compilation required.
	storePath, err := Build("nixpkgs#hello")
	if err != nil {
		t.Fatalf("Build(nixpkgs#hello): %v", err)
	}

	if !strings.HasPrefix(storePath, "/nix/store/") {
		t.Errorf("storePath = %q, want /nix/store/ prefix", storePath)
	}
	if !strings.Contains(storePath, "hello") {
		t.Errorf("storePath = %q, expected to contain \"hello\"", storePath)
	}

	// The path should actually exist after a successful build.
	if _, err := exec.Command("test", "-d", storePath).CombinedOutput(); err != nil {
		t.Errorf("store path does not exist on disk: %s", storePath)
	}
}

func TestBuild_InvalidFlakeRef(t *testing.T) {
	requireNix(t)

	_, err := Build("github:does-not-exist/definitely-not-a-real-flake")
	if err == nil {
		t.Fatal("expected error for invalid flake ref, got nil")
	}
}

func TestClosure_RealStorePath(t *testing.T) {
	requireNix(t)

	// First build something so we have a path with a known closure.
	storePath, err := Build("nixpkgs#hello")
	if err != nil {
		t.Fatalf("Build setup: %v", err)
	}

	closure, err := Closure(storePath)
	if err != nil {
		t.Fatalf("Closure(%s): %v", storePath, err)
	}

	if len(closure) == 0 {
		t.Fatal("closure is empty; expected at least the path itself")
	}

	// A closure always includes the path it was queried for.
	found := false
	for _, p := range closure {
		if p == storePath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("closure of %s does not include itself; got %v", storePath, closure)
	}

	// hello's closure includes glibc. Sanity-check the closure is non-trivial.
	foundGlibc := false
	for _, p := range closure {
		if strings.Contains(p, "glibc") {
			foundGlibc = true
			break
		}
	}
	if !foundGlibc {
		t.Logf("warning: closure does not include glibc; got %v", closure)
		t.Log("This might be fine on some architectures, but is suspicious.")
	}
}

func TestClosure_InvalidStorePath(t *testing.T) {
	requireNix(t)

	_, err := Closure("/nix/store/does-not-exist-no-such-path-zzzzzz")
	if err == nil {
		t.Fatal("expected error for non-existent store path, got nil")
	}
}
