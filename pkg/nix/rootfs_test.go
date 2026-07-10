package nix

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// readSpec deserializes the bundle's config.json after PrepareRootfs has
// modified it. Used to assert on the resulting spec.
func readSpec(t *testing.T, bundle string) ocispec.Spec {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}
	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatalf("unmarshal config.json: %v", err)
	}
	return spec
}

// findMount returns the mount with the given destination, or nil if absent.
func findMount(spec ocispec.Spec, destination string) *ocispec.Mount {
	for i, m := range spec.Mounts {
		if m.Destination == destination {
			return &spec.Mounts[i]
		}
	}
	return nil
}

func TestPrepareRootfs_AddsBindMounts(t *testing.T) {
	storePath := "/nix/store/abc-hello-2.12.3"
	closure := []string{
		"/nix/store/abc-hello-2.12.3",
		"/nix/store/def-glibc-2.42",
		"/nix/store/ghi-libidn2-2.3.8",
	}
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:    &ocispec.Root{Path: "rootfs"},
	})

	err := PrepareRootfs(bundle, FlakeOutput{StorePath: storePath}, &ContainixConfig{
		FlakeRef:   "nixpkgs#hello",
		Entrypoint: "bin/hello",
	}, closure)
	if err != nil {
		t.Fatalf("PrepareRootfs: %v", err)
	}

	spec := readSpec(t, bundle)

	// One mount per closure path, with correct fields.
	for _, sp := range closure {
		base := filepath.Base(sp)
		dest := filepath.Join("/nix/store", base)
		m := findMount(spec, dest)
		if m == nil {
			t.Errorf("missing mount for %s", dest)
			continue
		}
		if m.Source != sp {
			t.Errorf("mount %s: source = %q, want %q", dest, m.Source, sp)
		}
		if m.Type != "bind" {
			t.Errorf("mount %s: type = %q, want %q", dest, m.Type, "bind")
		}
		// Options must include both rbind and ro.
		hasRbind, hasRO := false, false
		for _, o := range m.Options {
			if o == "rbind" {
				hasRbind = true
			}
			if o == "ro" {
				hasRO = true
			}
		}
		if !hasRbind || !hasRO {
			t.Errorf("mount %s: options = %v, want both rbind and ro", dest, m.Options)
		}
	}
}

func TestPrepareRootfs_PreservesExistingMounts(t *testing.T) {
	// Containerd's default spec includes /proc, /sys, /dev, etc. PrepareRootfs
	// must not drop them when adding Nix store mounts.
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:    &ocispec.Root{Path: "rootfs"},
		Mounts: []ocispec.Mount{
			{Destination: "/proc", Type: "proc", Source: "proc"},
			{Destination: "/sys", Type: "sysfs", Source: "sysfs"},
			{Destination: "/dev", Type: "tmpfs", Source: "tmpfs"},
		},
	})

	// No entrypoint annotation, so resolution falls to meta.mainProgram.
	err := PrepareRootfs(bundle, FlakeOutput{StorePath: "/nix/store/abc-hello", MainProgram: "hello"}, &ContainixConfig{
		FlakeRef: "nixpkgs#hello",
	}, []string{"/nix/store/abc-hello"})
	if err != nil {
		t.Fatalf("PrepareRootfs: %v", err)
	}

	spec := readSpec(t, bundle)
	for _, dest := range []string{"/proc", "/sys", "/dev"} {
		if findMount(spec, dest) == nil {
			t.Errorf("default mount %s was dropped", dest)
		}
	}
}

func TestPrepareRootfs_RelativeEntrypoint(t *testing.T) {
	storePath := "/nix/store/abc-hello-2.12.3"
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}}, // image default
		Root:    &ocispec.Root{Path: "rootfs"},
	})

	err := PrepareRootfs(bundle, FlakeOutput{StorePath: storePath}, &ContainixConfig{
		FlakeRef:   "nixpkgs#hello",
		Entrypoint: "bin/hello", // relative
	}, []string{storePath})
	if err != nil {
		t.Fatalf("PrepareRootfs: %v", err)
	}

	spec := readSpec(t, bundle)
	want := "/nix/store/abc-hello-2.12.3/bin/hello"
	if len(spec.Process.Args) != 1 || spec.Process.Args[0] != want {
		t.Errorf("Process.Args = %v, want [%q]", spec.Process.Args, want)
	}
}

func TestPrepareRootfs_AbsoluteEntrypoint(t *testing.T) {
	// Absolute paths bypass storePath joining — escape hatch for pointing
	// at a binary outside the flake's output (e.g., bash from the closure).
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:    &ocispec.Root{Path: "rootfs"},
	})

	absolute := "/nix/store/xyz-bash-5.2/bin/bash"
	err := PrepareRootfs(bundle, FlakeOutput{StorePath: "/nix/store/abc-hello"}, &ContainixConfig{
		FlakeRef:   "nixpkgs#hello",
		Entrypoint: absolute,
	}, []string{"/nix/store/abc-hello"})
	if err != nil {
		t.Fatalf("PrepareRootfs: %v", err)
	}

	spec := readSpec(t, bundle)
	if len(spec.Process.Args) != 1 || spec.Process.Args[0] != absolute {
		t.Errorf("Process.Args = %v, want [%q]", spec.Process.Args, absolute)
	}
}

func TestPrepareRootfs_NoEntrypointNoMainProgramErrors(t *testing.T) {
	// With neither an entrypoint annotation nor meta.mainProgram, the three-tier
	// waterfall has nothing to resolve. PrepareRootfs must error rather than
	// silently leaving the image default — the flake is the source of truth.
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:    &ocispec.Root{Path: "rootfs"},
	})

	err := PrepareRootfs(bundle, FlakeOutput{StorePath: "/nix/store/abc-hello"}, &ContainixConfig{
		FlakeRef:   "nixpkgs#hello",
		Entrypoint: "", // unset, and FlakeOutput has no MainProgram
	}, []string{"/nix/store/abc-hello"})
	if err == nil {
		t.Fatal("expected error when neither entrypoint nor mainProgram is set, got nil")
	}
}

func TestPrepareRootfs_PersistsToDisk(t *testing.T) {
	// Verify the modifications actually round-trip through the file system —
	// not just stored in an in-memory spec. We do this by reading the file
	// directly (not via readSpec helper) to ensure config.json on disk has
	// our mounts.
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:    &ocispec.Root{Path: "rootfs"},
	})

	err := PrepareRootfs(bundle, FlakeOutput{StorePath: "/nix/store/abc-hello"}, &ContainixConfig{
		FlakeRef:   "nixpkgs#hello",
		Entrypoint: "bin/hello",
	}, []string{"/nix/store/abc-hello", "/nix/store/def-glibc"})
	if err != nil {
		t.Fatalf("PrepareRootfs: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(bundle, "config.json"))
	if err != nil {
		t.Fatalf("read config.json: %v", err)
	}

	// The serialized spec should contain references to both store paths.
	body := string(raw)
	for _, sp := range []string{"abc-hello", "def-glibc"} {
		if !contains(body, sp) {
			t.Errorf("config.json missing store path %q in body", sp)
		}
	}
	if !contains(body, "/nix/store/abc-hello/bin/hello") {
		t.Error("config.json missing resolved entrypoint")
	}
}

func TestPrepareRootfs_MissingConfigFile(t *testing.T) {
	dir := t.TempDir()
	// No config.json written — should error cleanly.

	err := PrepareRootfs(dir, FlakeOutput{StorePath: "/nix/store/abc-hello"}, &ContainixConfig{
		FlakeRef: "nixpkgs#hello",
	}, []string{"/nix/store/abc-hello"})
	if err == nil {
		t.Fatal("expected error for missing config.json, got nil")
	}
}

func TestPrepareRootfs_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("not valid {"), 0o644); err != nil {
		t.Fatalf("write config.json: %v", err)
	}

	err := PrepareRootfs(dir, FlakeOutput{StorePath: "/nix/store/abc-hello"}, &ContainixConfig{
		FlakeRef: "nixpkgs#hello",
	}, []string{"/nix/store/abc-hello"})
	if err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

func TestPrepareRootfs_EmptyClosure(t *testing.T) {
	// An empty closure shouldn't crash; just no mounts added.
	bundle := writeSpec(t, ocispec.Spec{
		Process: &ocispec.Process{Args: []string{"/bin/sh"}},
		Root:    &ocispec.Root{Path: "rootfs"},
	})

	err := PrepareRootfs(bundle, FlakeOutput{StorePath: "/nix/store/abc-hello"}, &ContainixConfig{
		FlakeRef:   "nixpkgs#hello",
		Entrypoint: "bin/hello", // needed so resolution succeeds; test targets the empty closure
	}, []string{})
	if err != nil {
		t.Fatalf("PrepareRootfs: %v", err)
	}

	spec := readSpec(t, bundle)
	// No /nix/store mounts should have been added.
	for _, m := range spec.Mounts {
		if filepath.Dir(m.Destination) == "/nix/store" {
			t.Errorf("unexpected nix store mount with empty closure: %+v", m)
		}
	}
}

// contains is strings.Contains by another name; inlined to avoid an import
// since this test file already pulls in many standard packages.
func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
