# Tutorial 6: Production Hardening

Make containix robust enough to run real workloads. This tutorial covers the things that break in production and how to handle them.

## Goal

- Timeouts and failure handling for Nix operations
- Prometheus metrics from the shim
- FHS-compatible rootfs (no raw store paths in entrypoints)
- Default-package + `meta.mainProgram` conventions for zero-annotation pods
- Security considerations
- Operational runbooks

## Step 1: Timeouts

`nix build` can hang indefinitely — network issues, slow evaluation, broken derivations. The shim must enforce timeouts.

```go
// pkg/nix/build.go
func BuildWithTimeout(ctx context.Context, flakeRef string, timeout time.Duration) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "nix", "build",
		"--no-link",
		"--print-out-paths",
		"--no-update-lock-file",
		"--no-write-lock-file",
		flakeRef,
	)

	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("nix build timed out after %s for %s", timeout, flakeRef)
	}
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("nix build failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("nix build: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
```

Add a configurable timeout annotation:

```go
const AnnotationTimeout = "containix.dev/timeout"  // default: "120s"
```

## Step 2: Prometheus metrics

Expose metrics from the shim via a Unix socket or HTTP endpoint that a node-level exporter can scrape.

```go
// pkg/metrics/metrics.go
package metrics

import "github.com/prometheus/client_golang/prometheus"

var (
	BuildDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "containix_nix_build_duration_seconds",
			Help:    "Time to resolve and build a flake reference",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 120},
		},
		[]string{"flake_ref", "cached"},
	)

	ClosureSize = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Name: "containix_closure_paths_total",
			Help: "Number of store paths in the container closure",
		},
		[]string{"flake_ref"},
	)

	CreateErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "containix_create_errors_total",
			Help: "Errors during container creation",
		},
		[]string{"phase"},  // "build", "closure", "rootfs"
	)

	ActiveContainers = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "containix_active_containers",
			Help: "Number of currently running containix containers",
		},
	)
)
```

## Step 3: FHS-compatible rootfs

Raw Nix store paths as entrypoints are fragile and ugly. Build a profile-style environment that creates a conventional filesystem layout.

### Approach: Use buildEnv in the flake

Recommend (or enforce) that flakes expose a `containerEnv` output:

```nix
# In the user's flake
{
  packages.x86_64-linux.containerEnv = pkgs.buildEnv {
    name = "myapp-env";
    paths = [ myapp pkgs.bashInteractive pkgs.coreutils ];
    pathsToLink = [ "/bin" "/lib" "/etc" "/share" ];
  };
}
```

This creates a single store path with `/bin`, `/lib`, etc. containing symlinks into the actual packages. The shim can mount this as the rootfs root, and entrypoints become simple paths like `/bin/myapp`.

### Shim-side support

```go
// If the flake output has /bin, /lib, etc., symlink them into rootfs root
func linkEnvToRootfs(rootfsPath, envPath string) error {
	entries, err := os.ReadDir(envPath)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		src := filepath.Join(envPath, entry.Name())
		dst := filepath.Join(rootfsPath, entry.Name())

		// Don't overwrite essential dirs we already created
		if _, err := os.Lstat(dst); err == nil {
			continue
		}
		if err := os.Symlink(src, dst); err != nil {
			return fmt.Errorf("linking %s: %w", entry.Name(), err)
		}
	}
	return nil
}
```

Now the Pod spec becomes cleaner:

```yaml
annotations:
  containix.dev/flake: "github:myorg/myapp#containerEnv"
  containix.dev/entrypoint: "/bin/myapp"
```

## Step 4: Default package and `meta.mainProgram` conventions

The end state for UX is a pod spec with **one annotation**:

```yaml
annotations:
  containix.dev/flake: "github:myorg/myapp"
```

No entrypoint. No path hashes. No buildEnv reference. The shim figures out everything else by leveraging two Nix conventions that users should follow when authoring flakes for containix.

### The `packages.<system>.default` convention

When a Nix flake is referenced **without an attribute path** (e.g., `github:org/repo` vs. `github:org/repo#someName`), Nix resolves it to `packages.<system>.default`. This is the established convention for "the main thing this flake produces."

Users author their flakes accordingly:

```nix
{
  outputs = { self, nixpkgs, ... }: let
    system = "aarch64-linux";
    pkgs = nixpkgs.legacyPackages.${system};
  in {
    packages.${system}.default = pkgs.buildGoModule {
      pname = "myapp";
      version = "1.0";
      src = ./.;
      vendorHash = "sha256-...";
      meta.mainProgram = "myapp";   # ← see next section
    };
  };
}
```

This is also what `nix build .` (with no attribute) builds. Same convention everywhere.

### The `meta.mainProgram` attribute

Nix derivations can declare which binary in the output is the "main" one. This is what `nix run` uses to pick the executable — if `meta.mainProgram = "myapp"` is set, `nix run github:myorg/myapp` executes `$out/bin/myapp`.

Containix should read the same attribute and use it as the default entrypoint:

```go
// pkg/nix/build.go

type FlakeOutput struct {
    StorePath   string
    MainProgram string  // empty if the derivation didn't declare one
}

// Build resolves a flake reference, returning the store path AND metadata
// useful for entrypoint resolution.
func Build(ctx context.Context, flakeRef string) (FlakeOutput, error) {
    // 1. Build the flake output as usual
    out, err := exec.CommandContext(ctx, "nix", "build",
        "--no-link", "--print-out-paths",
        "--no-update-lock-file", "--no-write-lock-file",
        flakeRef,
    ).Output()
    if err != nil {
        return FlakeOutput{}, fmt.Errorf("nix build: %w", err)
    }
    storePath := strings.TrimSpace(string(out))

    // 2. Read meta.mainProgram off the flake's default package — best-effort.
    //
    // GOTCHA: the obvious form
    //     nix eval --raw "<ref>.meta.mainProgram or \"\""
    // does NOT work, and fails in a way that hides itself:
    //   - `or` is Nix *language* syntax; it is not valid inside an
    //     installable's attribute path.
    //   - `<ref>.meta...` mis-parses — the flake-ref/attr separator is `#`,
    //     not `.`, so `.meta` gets read as part of the ref.
    // It errors every single time. Pair that with a swallowed error
    // (metaOut, _ := ...) and mainProgram is silently always "" — a dead
    // feature that still passes a naive "flake without mainProgram" test.
    //
    // Instead, pass the flake ref as the installable and use `--apply` to run
    // a function over the resolved package, where `or ""` IS valid Nix. A bare
    // ref resolves to packages.<system>.default, just like `nix build`. This
    // yields a clean tri-state (verified against nixpkgs#hello / #zlib):
    //     exit 0 + value  → mainProgram declared
    //     exit 0 + ""     → package exists but declares no mainProgram
    //     exit non-zero   → genuine failure (bad ref, no default package)
    metaCmd := exec.CommandContext(ctx, "nix", "eval", "--raw",
        flakeRef, "--apply", `p: p.meta.mainProgram or ""`,
    )
    metaOut, err := metaCmd.Output()
    if err != nil {
        // Not "absent mainProgram" (that's exit 0 + "") — a real eval failure.
        // The build already succeeded, so log and continue; resolveEntrypoint
        // will emit a clear error if no entrypoint can be determined.
        log.Printf("[containix] meta.mainProgram eval failed for %s: %v", flakeRef, err)
    }
    mainProgram := strings.TrimSpace(string(metaOut))

    return FlakeOutput{StorePath: storePath, MainProgram: mainProgram}, nil
}
```

### Entrypoint resolution in priority order

With both conventions available, the entrypoint resolution becomes a three-tier waterfall:

```go
func resolveEntrypoint(cfg *ContainixConfig, flake FlakeOutput) (string, error) {
    switch {
    case strings.HasPrefix(cfg.Entrypoint, "/"):
        // Explicit absolute path — use as-is.
        return cfg.Entrypoint, nil

    case cfg.Entrypoint != "":
        // Explicit relative path — join against the flake's store path.
        return filepath.Join(flake.StorePath, cfg.Entrypoint), nil

    case flake.MainProgram != "":
        // Default: bin/<mainProgram> in the flake's output.
        return filepath.Join(flake.StorePath, "bin", flake.MainProgram), nil

    default:
        return "", fmt.Errorf(
            "no entrypoint resolved for flake %q:\n"+
                "  - no %q annotation provided\n"+
                "  - flake does not declare meta.mainProgram on its default package\n"+
                "fix: either add the annotation or set meta.mainProgram in the derivation",
            cfg.FlakeRef, AnnotationEntrypoint)
    }
}
```

### Three tiers of UX

| Annotation | What the shim does | User burden |
|---|---|---|
| just `containix.dev/flake=github:org/app` | Build `packages.${system}.default`, read `meta.mainProgram`, exec `$out/bin/$mainProgram` | None |
| `containix.dev/entrypoint=bin/foo` | Build the flake's default, exec `$out/bin/foo` | Specify a binary name |
| `containix.dev/entrypoint=/explicit/path` | Use the absolute path literally | Full path needed |

### The goal pod spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
  annotations:
    containix.dev/flake: "github:myorg/myapp"
spec:
  runtimeClassName: containix
  containers:
  - name: app
    image: containix/empty:1.0.0
```

If the flake's default package declares `meta.mainProgram = "myapp"`, containix execs `/nix/store/<hash>-myapp/bin/myapp` inside the container. No paths, no hashes, no entrypoints in the manifest. The flake is the single source of truth.

### Why this matters

This isn't just ergonomic polish — it's about **separating concerns correctly**:

- Pod specs say "run this flake."
- The flake's `flake.nix` says "this is what I produce and how to run it."
- The shim just connects them.

Without `meta.mainProgram` support, pod specs have to know the binary name. That's a leak — the binary name is a flake-internal detail that should change when the flake changes, without requiring pod-spec edits. With this convention, you can rename a binary in your flake without touching any pod manifests.

### Edge cases worth documenting

- **Multi-output flakes**: `packages.${system}.serverCli` and `packages.${system}.workerCli`. Users reference them explicitly: `containix.dev/flake=github:org/app#serverCli`. The `#default` convention still works for the "main" output.
- **Flakes without `meta.mainProgram`**: nixpkgs packages mostly have this set; user-authored flakes often don't. The error message in `resolveEntrypoint` should point users at the docs.
- **Derivations with multiple binaries**: `meta.mainProgram` picks ONE. Other binaries are still in `$out/bin/` — accessible via the absolute-path escape hatch, but not as a default.

## Step 5: Security

### Restrict Nix evaluation

Nix evaluation is Turing-complete and can make network requests. Restrict it:

```go
cmd := exec.Command("nix", "build",
	"--no-link",
	"--print-out-paths",
	"--no-update-lock-file",   // don't modify lock files
	"--no-write-lock-file",    // don't write lock files
	"--no-allow-import-from-derivation",  // prevent IFD
	flakeRef,
)
```

### Validate flake references

Don't allow arbitrary flake refs. An admission webhook or the shim itself should validate:

```go
func ValidateFlakeRef(ref string) error {
	// Allow: github:org/repo#output, path:/local/path#output
	// Deny: anything that looks like it's trying to escape
	if strings.Contains(ref, "..") {
		return fmt.Errorf("flake ref contains path traversal")
	}
	// Optionally: restrict to a set of allowed orgs/repos
	return nil
}
```

### Read-only store mounts

All Nix store paths are mounted `ro` (read-only). The container cannot modify its own rootfs, which is a security win over writable overlay filesystems.

### Run as non-root

The container process should run as a non-root user. This is configured in the OCI spec (and can be enforced by the shim):

```go
// Ensure the process doesn't run as root unless explicitly requested
if spec.Process.User.UID == 0 {
	// Set to nobody (65534) by default
	spec.Process.User.UID = 65534
	spec.Process.User.GID = 65534
}
```

## Step 6: Error handling and failure modes

### Nix daemon not running

```go
func checkNixDaemon() error {
	cmd := exec.Command("nix", "store", "ping")
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("nix daemon unreachable: %w", err)
	}
	return nil
}
```

Call this in the shim's startup. If the daemon isn't running, log clearly and fail early — don't let pods hang in `ContainerCreating`.

### Binary cache unreachable

If the binary cache is down but paths are already local, builds still succeed. But if paths need to be fetched, the build fails. The shim should:

1. Attempt the build
2. On failure, check if it's a network issue
3. Return a clear error to containerd (which surfaces as a pod event)

### Store path corruption

Rare, but possible. If a store path is corrupted:

```bash
nix-store --verify --check-contents --repair
```

The shim shouldn't attempt this automatically. Surface a metric and alert.

## Step 7: Operational runbook

### Pod stuck in ContainerCreating

```bash
# Check containerd logs
journalctl -u containerd | grep containix

# Common causes:
# - nix build timeout (increase containix.dev/timeout)
# - binary cache unreachable (check node network)
# - flake ref doesn't exist (check annotation spelling)
# - nix daemon down (systemctl status nix-daemon)
```

### Node disk full

```bash
# Check Nix store size
du -sh /nix/store

# GC with protection for running containers
# (GC roots from Tutorial 5 prevent eviction of live paths)
nix-collect-garbage --delete-older-than 3d

# Nuclear option: delete everything not referenced by a GC root
nix-collect-garbage -d
```

### Rolling out a new version of a flake

Pods reference flakes by ref (e.g., `github:org/repo#app`). To roll out:

1. Push the new code to the repo
2. Update the flake.lock (or pin to a specific commit in the annotation)
3. Delete and recreate the pod (or use a Deployment with `kubectl rollout restart`)

For pinned versions:
```yaml
containix.dev/flake: "github:myorg/myapp/<commit-sha>#default"
```

## What you've built

A production-hardened containix with:
- Timeout enforcement for Nix operations
- Prometheus metrics for observability
- FHS-compatible rootfs via buildEnv
- Security restrictions on evaluation and mount permissions
- Clear error handling and operational runbooks

## What's next

This tutorial series has taken you from "what's a containerd shim" to a production-capable runtime. From here:

- **Admission webhook**: Validate flake refs, inject defaults, resolve entrypoints automatically
- **CRD**: A `NixWorkload` custom resource that generates the Pod spec
- **Caching layer**: Cache flake evaluation results to skip `nix build` on repeated creates
- **Multi-arch**: Support `aarch64-linux` alongside `x86_64-linux`
- **Nix store sharing**: Mount a shared EBS/EFS volume as `/nix/store` across nodes in a node group
