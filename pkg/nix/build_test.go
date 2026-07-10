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

	out, err := Build(ctx, "nixpkgs#hello")
	if err != nil {
		t.Fatalf("Build(nixpkgs#hello): %v", err)
	}

	if !strings.HasPrefix(out.StorePath, "/nix/store/") {
		t.Errorf("StorePath = %q, want /nix/store/ prefix", out.StorePath)
	}
	if !strings.Contains(out.StorePath, "hello") {
		t.Errorf("StorePath = %q, expected to contain \"hello\"", out.StorePath)
	}
	// nixpkgs#hello declares meta.mainProgram = "hello"; Build must surface it.
	if out.MainProgram != "hello" {
		t.Errorf("MainProgram = %q, want \"hello\"", out.MainProgram)
	}

	if _, err := exec.Command("test", "-d", out.StorePath).CombinedOutput(); err != nil {
		t.Errorf("store path does not exist on disk: %s", out.StorePath)
	}
}

// TestResolveEntrypoint exercises the three-tier waterfall directly. It's a
// pure function (no Nix), so it runs even under -short.
func TestResolveEntrypoint(t *testing.T) {
	const store = "/nix/store/abc-hello"
	tests := []struct {
		name       string
		entrypoint string
		mainProg   string
		want       string
		wantErr    bool
	}{
		{"absolute path used as-is", "/usr/bin/env", "", "/usr/bin/env", false},
		{"relative path joined to store", "bin/hello", "", "/nix/store/abc-hello/bin/hello", false},
		{"mainProgram default", "", "hello", "/nix/store/abc-hello/bin/hello", false},
		{"explicit entrypoint beats mainProgram", "bin/other", "hello", "/nix/store/abc-hello/bin/other", false},
		{"nothing to resolve is an error", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &ContainixConfig{FlakeRef: "nixpkgs#hello", Entrypoint: tt.entrypoint}
			flake := FlakeOutput{StorePath: store, MainProgram: tt.mainProg}
			got, err := resolveEntrypoint(cfg, flake)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %q", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
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

	out, err := Build(ctx, "nixpkgs#hello")
	if err != nil {
		t.Fatalf("Build setup: %v", err)
	}

	closure, err := Closure(ctx, out.StorePath)
	if err != nil {
		t.Fatalf("Closure(%s): %v", out.StorePath, err)
	}

	if len(closure) == 0 {
		t.Fatal("closure is empty; expected at least the path itself")
	}

	found := false
	for _, p := range closure {
		if p == out.StorePath {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("closure of %s does not include itself; got %v", out.StorePath, closure)
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
