package nix

import (
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
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
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	storePath, err := Build(ctx, "nixpkgs#hello")
	if err != nil {
		t.Fatalf("Build(nixpkgs#hello): %v", err)
	}

	if !strings.HasPrefix(storePath, "/nix/store/") {
		t.Errorf("storePath = %q, want /nix/store/ prefix", storePath)
	}
	if !strings.Contains(storePath, "hello") {
		t.Errorf("storePath = %q, expected to contain \"hello\"", storePath)
	}

	if _, err := exec.Command("test", "-d", storePath).CombinedOutput(); err != nil {
		t.Errorf("store path does not exist on disk: %s", storePath)
	}
}

func TestBuild_InvalidFlakeRef(t *testing.T) {
	requireNix(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := Build(ctx, "github:does-not-exist/definitely-not-a-real-flake")
	if err == nil {
		t.Fatal("expected error for invalid flake ref, got nil")
	}
}

func TestBuild_TimeoutKillsSubprocess(t *testing.T) {
	requireNix(t)

	// A 1-nanosecond deadline guarantees the context is expired before nix
	// can do anything meaningful. We expect ErrTimeout, not a "real" error.
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()

	start := time.Now()
	_, err := Build(ctx, "nixpkgs#hello")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Errorf("expected ErrTimeout, got %v", err)
	}
	// Should fail fast — not hang for the full nix-build time.
	if elapsed > 10*time.Second {
		t.Errorf("Build took %s; should have been killed quickly by ctx", elapsed)
	}
}

func TestClosure_RealStorePath(t *testing.T) {
	requireNix(t)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	storePath, err := Build(ctx, "nixpkgs#hello")
	if err != nil {
		t.Fatalf("Build setup: %v", err)
	}

	closure, err := Closure(ctx, storePath)
	if err != nil {
		t.Fatalf("Closure(%s): %v", storePath, err)
	}

	if len(closure) == 0 {
		t.Fatal("closure is empty; expected at least the path itself")
	}

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
}

func TestClosure_InvalidStorePath(t *testing.T) {
	requireNix(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	_, err := Closure(ctx, "/nix/store/does-not-exist-no-such-path-zzzzzz")
	if err == nil {
		t.Fatal("expected error for non-existent store path, got nil")
	}
}
