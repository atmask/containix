<p align="center">
  <img src="assets/logo.svg" alt="containix logo" width="600"/>
</p>

<p align="center">
  <a href="https://github.com/atmask/containix/actions/workflows/ci.yml"><img src="https://github.com/atmask/containix/actions/workflows/ci.yml/badge.svg" alt="CI"/></a>
  <a href="https://pkg.go.dev/github.com/atmask/containix"><img src="https://pkg.go.dev/badge/github.com/atmask/containix.svg" alt="Go Reference"/></a>
  <a href="./LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License: Apache 2.0"/></a>
  <a href="https://nixos.wiki/wiki/Flakes"><img src="https://img.shields.io/badge/nix-flake-blue?logo=nixos" alt="Nix Flake"/></a>
</p>

A **containerd shim that runs Nix flakes directly on Kubernetes** — no container images required.

Instead of building, pushing, and pulling OCI images, containix lets you reference a Nix flake in your Pod spec. The shim resolves the flake, fetches the closure from your binary cache, assembles a rootfs from Nix store paths, and hands off to runc. Pods start with exactly the packages they need — nothing more.

> **Status: experimental.** Validated end-to-end on NixOS aarch64 + k3s with ~91% test coverage across core packages. The API will change before v1; pin commits, not branches. Production use is not recommended yet.

Project site: **[containix.dev](https://containix.dev)**.

---

## How it works

```
Pod spec (annotation: containix.dev/flake = "github:org/repo#app")
  → containerd invokes containix shim
    → shim resolves flake to store paths
      → bind-mounts Nix closure as container rootfs
        → runc executes the container
```

containix registers as a [containerd shim v2](https://github.com/containerd/containerd/blob/main/core/runtime/v2/README.md) runtime. A Kubernetes `RuntimeClass` routes opted-in pods to the shim. A near-empty stub image satisfies the CRI/OCI spec — the real filesystem comes from Nix.

---

## Pod spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
  annotations:
    containix.dev/flake: "github:myorg/myapp#default"
    containix.dev/entrypoint: "bin/myapp"
spec:
  runtimeClassName: containix
  containers:
    - name: app
      image: "ghcr.io/your-org/containix-empty:1.0.0"
```

`containix.dev/entrypoint` is resolved relative to the flake's output store path (`bin/myapp` → `/nix/store/<hash>-myapp/bin/myapp`). Absolute paths starting with `/` are honored as-is.

---

## Node requirements

- containerd (default on EKS, GKE, most managed k8s; bundled in k3s).
- Nix daemon running on the node.
- Access to a binary cache (Cachix, Attic, or self-hosted) for cold-start performance.
- A reachable OCI stub image (CRI requires `image:` even though it isn't used).

---

## Installation (NixOS)

Add containix as a flake input and enable the module:

```nix
{
  inputs.containix.url = "github:atmask/containix";

  outputs = inputs @ { nixpkgs, containix, ... }: {
    nixosConfigurations.your-host = nixpkgs.lib.nixosSystem {
      modules = [
        ./your-host.nix
        containix.nixosModules.default
        { services.containix.enable = true; }
      ];
    };
  };
}
```

The module installs the shim binary, configures containerd to register `runtime_type = "io.containerd.containix.v1"`, and ensures `runc` and `nix` are on containerd's PATH. After `nixos-rebuild switch`:

```bash
sudo containerd config dump | grep -A 3 containix
sudo ctr run --rm \
  --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="nixpkgs#hello" \
  --label containix.dev/entrypoint="bin/hello" \
  docker.io/library/alpine:latest containix-smoke-test
```

Non-NixOS deployments need to install the binary and configure containerd manually.

---

## Architecture

```
┌─────────────────────────────────────────────────┐
│ Kubernetes API Server                           │
│   Pod spec with runtimeClassName: containix     │
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│ containerd                                      │
│   routes to containix shim via RuntimeClass     │
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│ containix shim (containerd-shim-containix-v1)   │
│                                                 │
│  1. Read flake ref from container annotations   │
│  2. nix build --print-out-paths <flake-ref>     │
│  3. nix-store -qR <path> → full closure         │
│  4. Assemble rootfs (bind-mount store paths)    │
│  5. Generate OCI runtime spec                   │
│  6. Invoke runc                                 │
└──────────────────────┬──────────────────────────┘
                       │
┌──────────────────────▼──────────────────────────┐
│ runc                                            │
│   standard container lifecycle                  │
└─────────────────────────────────────────────────┘
```

---

## Related work

containix is one of several approaches to running Nix-built workloads on Kubernetes. Honest framing:

- **[nix-snapshotter](https://github.com/pdtpartners/nix-snapshotter)** — implements containerd's snapshotter interface (vs. containix's shim interface). More mature, used in production by Flox. Different architectural fit: snapshotter changes how rootfs is prepared; shim controls per-container lifecycle (Create, Wait, Exec, Kill). Containix gives more lifecycle control at the cost of more surface area.
- **[nix2container](https://github.com/nlewo/nix2container)** — produces real OCI images from Nix flakes, with each store path as a layer. Stays within the OCI image ecosystem, so it works with existing registries, image scanners, and node-level tooling. No node-side Nix install required. Use this if you want Nix builds but standard image distribution.
- **[nixery.dev](https://nixery.dev)** — public registry that builds OCI images on demand from URL-encoded Nix package specs. Demo-friendly; not for production at scale.

containix wins when: you have a fleet sharing significant runtime dependencies and want per-package on-disk sharing (`/nix/store/<hash>-glibc/` mounted once on the node, bind-mounted into N pods). It loses when: you need broad ecosystem tooling around the artifact (image scanners, signing, registry policies), or when the node-side Nix dependency is a non-starter.

---

## Development

Nix-managed dev environment. With [Nix](https://nixos.org/download) and [direnv](https://direnv.net/) installed:

```bash
git clone https://github.com/atmask/containix
cd containix
direnv allow          # first time only — loads the Nix dev shell
```

Without direnv:

```bash
nix develop
```

The dev shell installs Go, just, gopls, golangci-lint, pre-commit, and the configured hooks (which auto-install into `.git/hooks/` on first shell entry).

Common commands (see [`justfile`](./justfile)):

| Command | Effect |
|---|---|
| `just build` | Cross-compile shim for linux/arm64 |
| `just install` | Build + install + restart containerd (Linux only) |
| `just test` | Smoke test (alpine + `nixpkgs#hello`, Linux only) |
| `nix develop -c go test ./...` | Run the Go test suite |
| `nix build .#default` | Build the shim binary via Nix |

Contributions: see [CONTRIBUTING.md](./CONTRIBUTING.md). Security: see [SECURITY.md](./SECURITY.md).

The [`docs/tutorials/`](./docs/tutorials/) directory contains a build journal that walks through containix's design from first principles. Useful background; not required reading.

---

## License

Copyright 2026 Ben Mask. Licensed under the [Apache License 2.0](./LICENSE). See [NOTICE](./NOTICE) for attribution.
