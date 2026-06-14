package nix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// writeSpec writes a synthetic config.json into a fresh temp dir and returns
// that dir as the "bundle path." Used by every test case.
func writeSpec(t *testing.T, spec ocispec.Spec) string {
	t.Helper()
	dir := t.TempDir()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("marshal spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), data, 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}
	return dir
}

func TestFromBundle_HappyPath(t *testing.T) {
	bundle := writeSpec(t, ocispec.Spec{
		Annotations: map[string]string{
			AnnotationFlake:      "nixpkgs#hello",
			AnnotationEntrypoint: "bin/hello",
		},
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg")
	}
	if cfg.FlakeRef != "nixpkgs#hello" {
		t.Errorf("FlakeRef = %q, want %q", cfg.FlakeRef, "nixpkgs#hello")
	}
	if cfg.Entrypoint != "bin/hello" {
		t.Errorf("Entrypoint = %q, want %q", cfg.Entrypoint, "bin/hello")
	}
}

func TestFromBundle_MissingEntrypoint(t *testing.T) {
	// Entrypoint is optional — flake alone is sufficient for the shim to
	// proceed (it would fall back to OCI defaults or meta.mainProgram later).
	bundle := writeSpec(t, ocispec.Spec{
		Annotations: map[string]string{
			AnnotationFlake: "nixpkgs#hello",
		},
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg (flake is present, entrypoint is optional)")
	}
	if cfg.FlakeRef != "nixpkgs#hello" {
		t.Errorf("FlakeRef = %q, want %q", cfg.FlakeRef, "nixpkgs#hello")
	}
	if cfg.Entrypoint != "" {
		t.Errorf("Entrypoint = %q, want empty string", cfg.Entrypoint)
	}
}

func TestFromBundle_MissingFlake(t *testing.T) {
	// Without containix.dev/flake, this isn't a containix container at all.
	// Return nil so the shim's Create handler passes through to runc unchanged.
	bundle := writeSpec(t, ocispec.Spec{
		Annotations: map[string]string{
			"some.other/annotation": "value",
		},
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil cfg for passthrough, got %+v", cfg)
	}
}

func TestFromBundle_SandboxContainer(t *testing.T) {
	// Sandbox containers (the pause container that holds pod-level namespaces)
	// should pass through to runc untouched, even if a flake annotation is
	// somehow present. The Nix logic only applies to app containers.
	bundle := writeSpec(t, ocispec.Spec{
		Annotations: map[string]string{
			AnnotationContainerType: "sandbox",
			AnnotationFlake:         "nixpkgs#hello", // would normally trigger Nix work
		},
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil cfg for sandbox container, got %+v", cfg)
	}
}

func TestFromBundle_AppContainerExplicitType(t *testing.T) {
	// Belt-and-suspenders: an explicit container-type=container should be
	// treated identically to no container-type annotation (since the default
	// expectation is "container," not "sandbox").
	bundle := writeSpec(t, ocispec.Spec{
		Annotations: map[string]string{
			AnnotationContainerType: "container",
			AnnotationFlake:         "nixpkgs#hello",
			AnnotationEntrypoint:    "bin/hello",
		},
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg for explicit container-type=container")
	}
	if cfg.FlakeRef != "nixpkgs#hello" {
		t.Errorf("FlakeRef = %q, want %q", cfg.FlakeRef, "nixpkgs#hello")
	}
}

func TestFromBundle_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not valid json{"), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	cfg, err := FromBundle(dir)
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
	if cfg != nil {
		t.Errorf("expected nil cfg on error, got %+v", cfg)
	}
}

func TestFromBundle_MissingConfigFile(t *testing.T) {
	dir := t.TempDir()
	// No config.json written.

	cfg, err := FromBundle(dir)
	if err == nil {
		t.Fatal("expected error for missing config.json, got nil")
	}
	if cfg != nil {
		t.Errorf("expected nil cfg on error, got %+v", cfg)
	}
}

func TestFromBundle_DefaultTimeoutWhenAbsent(t *testing.T) {
	bundle := writeSpec(t, ocispec.Spec{
		Annotations: map[string]string{
			AnnotationFlake: "nixpkgs#hello",
		},
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Timeout != DefaultBuildTimeout {
		t.Errorf("Timeout = %s, want default %s", cfg.Timeout, DefaultBuildTimeout)
	}
}

func TestFromBundle_TimeoutParsing(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected time.Duration
	}{
		{"explicit seconds", "30s", 30 * time.Second},
		{"explicit minutes", "5m", 5 * time.Minute},
		{"compound under cap", "10m30s", 10*time.Minute + 30*time.Second},
		{"empty falls back to default", "", DefaultBuildTimeout},
		{"malformed falls back to default", "not a duration", DefaultBuildTimeout},
		{"negative falls back to default", "-1s", DefaultBuildTimeout},
		{"zero falls back to default", "0s", DefaultBuildTimeout},
		{"above cap clamps to max", "1h30m", MaxBuildTimeout},
		{"absurdly large clamps to max", "100h", MaxBuildTimeout},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			annotations := map[string]string{AnnotationFlake: "nixpkgs#hello"}
			if tc.raw != "" {
				annotations[AnnotationTimeout] = tc.raw
			}
			bundle := writeSpec(t, ocispec.Spec{Annotations: annotations})

			cfg, err := FromBundle(bundle)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if cfg.Timeout != tc.expected {
				t.Errorf("raw=%q: Timeout = %s, want %s", tc.raw, cfg.Timeout, tc.expected)
			}
		})
	}
}

func TestFromBundle_EmptyAnnotations(t *testing.T) {
	bundle := writeSpec(t, ocispec.Spec{
		// Annotations is nil, not just empty.
	})

	cfg, err := FromBundle(bundle)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg != nil {
		t.Errorf("expected nil cfg for empty annotations, got %+v", cfg)
	}
}
