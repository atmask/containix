# Tutorial 2: Nix Rootfs

Intercept the shim's `Create` method to resolve a Nix flake reference and assemble the container rootfs from Nix store paths instead of an OCI image.

## Goal

When a container is created with a `containix.dev/flake` annotation, the shim:
1. Reads the flake reference from the OCI spec annotations
2. Builds the flake output with `nix build`
3. Computes the full closure with `nix-store -qR`
4. Assembles a rootfs by bind-mounting the closure
5. Passes the modified rootfs to runc

Containers without the annotation pass through to standard runc behavior.

## The problem: Nix closures aren't FHS

A Nix closure looks like this:

```
/nix/store/abc123-glibc-2.39/lib/libc.so.6
/nix/store/def456-hello-2.12/bin/hello
/nix/store/ghi789-bash-5.2/bin/bash
```

But a container expects an FHS rootfs:

```
/bin/hello
/lib/libc.so.6
/usr/bin/bash
```

We have two options:

### Option A: Mount the Nix store into the container

Mount `/nix/store` read-only into the container rootfs. The entrypoint uses full Nix store paths. This is simpler and what we'll do first.

```
container rootfs:
  /nix/store/abc123-glibc-2.39/...  (bind mount)
  /nix/store/def456-hello-2.12/...  (bind mount)
  /nix/store/ghi789-bash-5.2/...    (bind mount)

entrypoint: /nix/store/def456-hello-2.12/bin/hello
```

### Option B: Build a profile-style symlink tree

Use `nix-env --profile` or `buildEnv` to create an FHS-like tree with symlinks into the store. More polished, but adds build complexity.

We'll start with Option A and revisit in Tutorial 6.

## Step 1: Reading annotations from the OCI spec

When containerd calls `Create`, the bundle directory contains a `config.json` (OCI runtime spec). Annotations from the Pod spec flow through to this file.

```go
// pkg/nix/annotations.go
package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	AnnotationFlake      = "containix.dev/flake"
	AnnotationEntrypoint = "containix.dev/entrypoint"
)

// ContainixConfig captures the containix-specific annotations from the bundle.
// Entrypoint is interpreted as a path relative to the flake's resolved store
// path (e.g. "bin/hello"). If you really need to point at a different
// location, prefix with "/" for an absolute path.
type ContainixConfig struct {
	FlakeRef   string
	Entrypoint string
}

// FromBundle reads containix annotations from the OCI bundle's config.json.
// Returns nil if no containix annotations are present (passthrough to runc).
func FromBundle(bundlePath string) (*ContainixConfig, error) {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("reading OCI spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing OCI spec: %w", err)
	}

	flakeRef, ok := spec.Annotations[AnnotationFlake]
	if !ok {
		return nil, nil // not a containix container, passthrough
	}

	entrypoint := spec.Annotations[AnnotationEntrypoint]

	return &ContainixConfig{
		FlakeRef:   flakeRef,
		Entrypoint: entrypoint,
	}, nil
}
```

## Step 2: Resolving the flake and computing the closure

```go
// pkg/nix/build.go
package nix

import (
	"fmt"
	"os/exec"
	"strings"
)

// Build resolves a flake reference to a Nix store path.
func Build(flakeRef string) (string, error) {
	cmd := exec.Command("nix", "build",
		"--no-link",
		"--print-out-paths",
		"--no-update-lock-file",
		"--no-write-lock-file",
		flakeRef,
	)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("nix build failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("nix build: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Closure returns all store paths in the closure of the given path.
func Closure(storePath string) ([]string, error) {
	cmd := exec.Command("nix-store", "-qR", storePath)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("nix-store -qR: %w", err)
	}

	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
```

## Step 3: Assembling the rootfs

The rootfs needs to contain the Nix store paths. We bind-mount each closure path into the container's rootfs under `/nix/store/`.

Two things this function gets right that you might miss in a first pass:

1. **The rootfs directory comes from `spec.Root.Path`, not a hardcoded `"rootfs"`.** Convention is to call it `rootfs/`, but the OCI spec allows any path — relative to the bundle (the usual case) or absolute. Reading the spec is the correct OCI-compliant way to find it.
2. **The entrypoint annotation is resolved against the flake's store path.** Users write `bin/hello`, not `/nix/store/abc-hello-2.12.1/bin/hello`. The shim does the join so pod specs don't have to know about hashes. Absolute entrypoints (starting with `/`) are still honored as an escape hatch.

```go
// pkg/nix/rootfs.go
package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// PrepareRootfs modifies the OCI bundle to mount Nix store paths
// and resolve the entrypoint against the flake's store path.
func PrepareRootfs(bundlePath, storePath string, cfg *ContainixConfig, closurePaths []string) error {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("reading spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parsing spec: %w", err)
	}

	// Resolve the rootfs path from the OCI spec. Per the OCI runtime spec,
	// Root.Path may be relative to the bundle (the common case, usually
	// "rootfs") or absolute. Honor both.
	rootfs := spec.Root.Path
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(bundlePath, rootfs)
	}

	// Create /nix/store in the rootfs
	nixStore := filepath.Join(rootfs, "nix", "store")
	if err := os.MkdirAll(nixStore, 0755); err != nil {
		return fmt.Errorf("creating /nix/store in rootfs: %w", err)
	}

	// Create mount targets and add bind mounts for each closure path.
	// runc will create the destination directories inside the container's
	// mount namespace at start time if they don't exist — we don't need to
	// pre-create them on the host. Doing so would leave empty dirs on the
	// host's bundle/rootfs that containerd can't clean up later.
	for _, sp := range closurePaths {
		base := filepath.Base(sp)
		spec.Mounts = append(spec.Mounts, ocispec.Mount{
			Destination: filepath.Join("/nix/store", base),
			Source:      sp,
			Type:        "bind",
			Options:     []string{"rbind", "ro"},
		})
	}

	// Ensure the OS pseudo-filesystems are mounted. Without /proc and /sys,
	// many glibc code paths hang or fail. ctr's default OCI spec sometimes
	// includes these, sometimes doesn't — make sure they're present without
	// duplicating entries.
	ensureMount(&spec, ocispec.Mount{
		Destination: "/proc",
		Type:        "proc",
		Source:      "proc",
		Options:     []string{"nosuid", "noexec", "nodev"},
	})
	ensureMount(&spec, ocispec.Mount{
		Destination: "/sys",
		Type:        "sysfs",
		Source:      "sysfs",
		Options:     []string{"nosuid", "noexec", "nodev", "ro"},
	})
	ensureMount(&spec, ocispec.Mount{
		Destination: "/dev",
		Type:        "tmpfs",
		Source:      "tmpfs",
		Options:     []string{"nosuid", "strictatime", "mode=755", "size=65536k"},
	})
	ensureMount(&spec, ocispec.Mount{
		Destination: "/tmp",
		Type:        "tmpfs",
		Source:      "tmpfs",
		Options:     []string{"nosuid", "nodev", "mode=1777"},
	})

	// Resolve and override entrypoint. The user supplies a path relative
	// to the flake's store output (e.g. "bin/hello") so pod specs don't
	// have to embed store hashes. Absolute paths are honored as-is for
	// the cases where you really need to point elsewhere in /nix/store.
	if cfg.Entrypoint != "" {
		entrypoint := cfg.Entrypoint
		if !strings.HasPrefix(entrypoint, "/") {
			entrypoint = filepath.Join(storePath, entrypoint)
		}
		spec.Process.Args = []string{entrypoint}
	}

	// Write modified spec back
	modified, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshaling spec: %w", err)
	}
	if err := os.WriteFile(specPath, modified, 0644); err != nil {
		return fmt.Errorf("writing spec: %w", err)
	}

	return nil
}

// ensureMount adds m to spec.Mounts if no existing mount targets the same
// destination. Idempotent — safe even when the upstream OCI spec already
// provides /proc, /sys, etc.
func ensureMount(spec *ocispec.Spec, m ocispec.Mount) {
	for _, existing := range spec.Mounts {
		if existing.Destination == m.Destination {
			return
		}
	}
	spec.Mounts = append(spec.Mounts, m)
}
```

## Step 4: Wire it into the shim's Create method

Update the `Create` method on the `Service` wrapper from Tutorial 1. Remember the wrapping pattern: `Service` embeds `taskAPI.TTRPCTaskService`, so forwarding is `s.TTRPCTaskService.Create(...)`.

```go
// pkg/shimservice/service.go
package shimservice

import (
	"context"
	"fmt"
	"log"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"

	"github.com/atmask/containix/pkg/nix"
)

type Service struct {
	taskAPI.TTRPCTaskService
}

func (s *Service) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	log.Printf("[containix] Create called: id=%s bundle=%s", req.ID, req.Bundle)

	// Read annotations. Returns nil if this isn't a containix container,
	// or if it's the pod sandbox (container-type=sandbox) — both pass through.
	cfg, err := nix.FromBundle(req.Bundle)
	if err != nil {
		log.Printf("[containix] error reading annotations: %v", err)
		return nil, err
	}

	if cfg != nil {
		log.Printf("[containix] Nix flake detected: %s", cfg.FlakeRef)

		storePath, err := nix.Build(cfg.FlakeRef)
		if err != nil {
			return nil, fmt.Errorf("nix build: %w", err)
		}
		log.Printf("[containix] store path: %s", storePath)

		closure, err := nix.Closure(storePath)
		if err != nil {
			return nil, fmt.Errorf("nix closure: %w", err)
		}
		log.Printf("[containix] closure: %d paths", len(closure))

		if err := nix.PrepareRootfs(req.Bundle, storePath, cfg, closure); err != nil {
			return nil, fmt.Errorf("preparing rootfs: %w", err)
		}
	}

	return s.TTRPCTaskService.Create(ctx, req)
}
```

### Branching on container-type

`Create` will be called multiple times per pod: once for the **sandbox container** (which runs the upstream `pause` binary and just holds the pod's shared namespaces), then once per **app container**. Only app containers should trigger Nix logic.

Update `pkg/nix/annotations.go` to filter on container-type:

```go
const (
	AnnotationFlake         = "containix.dev/flake"
	AnnotationEntrypoint    = "containix.dev/entrypoint"
	AnnotationContainerType = "io.kubernetes.cri.container-type"  // injected by CRI
)

func FromBundle(bundlePath string) (*ContainixConfig, error) {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("reading OCI spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing OCI spec: %w", err)
	}

	// Skip sandbox containers — they need the upstream pause image, not Nix.
	if spec.Annotations[AnnotationContainerType] == "sandbox" {
		return nil, nil
	}

	flakeRef, ok := spec.Annotations[AnnotationFlake]
	if !ok {
		return nil, nil // not a containix container, passthrough
	}

	return &ContainixConfig{
		FlakeRef:   flakeRef,
		Entrypoint: spec.Annotations[AnnotationEntrypoint],
	}, nil
}
```

## Step 5: Test it

```bash
# Build and install (use the project's justfile if you have one)
just install
# or equivalently:
#   go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
#   sudo install -m 755 bin/containerd-shim-containix-v1 /var/lib/containix/bin/
#   sudo systemctl restart containerd

# Smoke test with nixpkgs#hello — no need to look up store paths anymore.
# Entrypoint is relative to whatever the flake produces as its output.
sudo ctr run \
  --rm \
  --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="nixpkgs#hello" \
  --label containix.dev/entrypoint="bin/hello" \
  docker.io/library/alpine:latest \
  containix-smoke-test

# Should print:  Hello, world!

# Watch shim logs in another terminal to see the lifecycle:
sudo journalctl -u containerd -f | grep containix
```

Expected output:

```
Hello, world!
```

Expected log lines:

```
[containix] Create called: id=containix-smoke-test bundle=/run/containerd/...
[containix] Nix flake detected: nixpkgs#hello
[containix] store path: /nix/store/<hash>-hello-2.12.x
[containix] closure: 5 paths
```

The container exits immediately (hello prints and returns). If you don't see "Hello, world!" within a few seconds, see the debugging section in Tutorial 3.

## A test flake

Create `test-flake/flake.nix`:

```nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { nixpkgs, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      packages.${system}.default = pkgs.hello;
    };
}
```

## What you've built

The core of containix: a shim that resolves Nix flake references and assembles container rootfs from Nix store paths. The container runs with real Nix packages, no OCI image layers involved.

## Known limitations at this point

- **Cold start is slow**: `nix build` on first run fetches from the binary cache. We address this in Tutorial 5.
- **No caching of resolved flakes**: every `Create` runs `nix build` from scratch. Even when the flake's already built locally, `nix build` re-evaluates the flake.nix — measurable latency for large flakes. We'll add a resolution cache later.
- **Entrypoint annotation is still required.** Today the user has to provide `containix.dev/entrypoint=bin/hello`. The end state should be: shim reads `meta.mainProgram` from the derivation (what `nix run` does) and the annotation becomes optional — for well-formed flakes, just `containix.dev/flake=<ref>` is enough. The relative-path step in this tutorial is a meaningful improvement over absolute store paths but not the final UX. Tutorial 5 or 6 territory.
- **Entrypoint is relative to one store path**: works for single-derivation flakes. Multi-output flakes (e.g., a flake exposing both `packages.default` and `packages.cli`) would benefit from being able to reference different outputs in the entrypoint. Not a problem for Tutorial 2 scope.
- **Non-FHS rootfs**: the container sees `/nix/store/...` paths but no conventional `/bin`, `/usr`, `/lib`. Apps that hardcode `/etc/myapp/config.yaml` won't find their config. Tutorial 6 adds an FHS-like view via `buildEnv` for apps that need it.
- **Annotations vs dedicated fields**: using OCI annotations works but isn't discoverable. A CRD or admission webhook could improve UX.

## Next

[Tutorial 3: Local Integration](./03-local-integration.md) — run a Nix-backed container end-to-end with containerd.
