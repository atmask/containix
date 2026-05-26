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

```go
// pkg/nix/rootfs.go
package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// PrepareRootfs modifies the OCI bundle to mount Nix store paths
// and set the entrypoint.
func PrepareRootfs(bundlePath string, cfg *ContainixConfig, storePaths []string) error {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("reading spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parsing spec: %w", err)
	}

	// Create /nix/store in the rootfs
	rootfs := filepath.Join(bundlePath, "rootfs")
	nixStore := filepath.Join(rootfs, "nix", "store")
	if err := os.MkdirAll(nixStore, 0755); err != nil {
		return fmt.Errorf("creating /nix/store in rootfs: %w", err)
	}

	// Create mount targets and add bind mounts for each store path
	for _, sp := range storePaths {
		base := filepath.Base(sp)
		mountpoint := filepath.Join(nixStore, base)

		// Create mount target directory
		if err := os.MkdirAll(mountpoint, 0755); err != nil {
			return fmt.Errorf("creating mount target %s: %w", mountpoint, err)
		}

		// Add bind mount to OCI spec
		spec.Mounts = append(spec.Mounts, ocispec.Mount{
			Destination: filepath.Join("/nix/store", base),
			Source:      sp,
			Type:        "bind",
			Options:     []string{"rbind", "ro"},
		})
	}

	// Create essential directories that processes expect
	for _, dir := range []string{"tmp", "proc", "sys", "dev", "etc"} {
		if err := os.MkdirAll(filepath.Join(rootfs, dir), 0755); err != nil {
			return fmt.Errorf("creating /%s: %w", dir, err)
		}
	}

	// Override entrypoint if specified
	if cfg.Entrypoint != "" {
		spec.Process.Args = []string{cfg.Entrypoint}
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

		if err := nix.PrepareRootfs(req.Bundle, cfg, closure); err != nil {
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
# Build
CGO_ENABLED=0 go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
sudo cp bin/containerd-shim-containix-v1 /usr/local/bin/

# Build a test flake with a simple hello world
# (see test-flake/ in this repo for an example)

# Run with ctr, passing annotations
sudo ctr run \
  --rm \
  --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="path:/path/to/test-flake#default" \
  --label containix.dev/entrypoint="/nix/store/xxx-hello-2.12/bin/hello" \
  docker.io/library/alpine:latest \
  test-nix-rootfs

# Check logs
sudo journalctl -u containerd -f | grep containix
```

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
- **Full Nix store paths as entrypoints**: Not ergonomic. The entrypoint is something like `/nix/store/abc123-hello-2.12/bin/hello`. We could improve this with wrapper scripts or profile links (Tutorial 6).
- **No caching of resolved flakes**: Every `Create` runs `nix build` from scratch. We'll add a resolution cache later.
- **Annotations vs dedicated fields**: Using OCI annotations works but isn't discoverable. A CRD or admission webhook could improve UX.

## Next

[Tutorial 3: Local Integration](./03-local-integration.md) — run a Nix-backed container end-to-end with containerd.
