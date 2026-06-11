<p align="center">
  <img src="assets/logo.svg" alt="containix logo" width="600"/>
</p>

<div align="center">

[![CI](https://github.com/atmask/containix/actions/workflows/ci.yml/badge.svg)](https://github.com/atmask/containix/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/atmask/containix.svg)](https://pkg.go.dev/github.com/atmask/containix)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](./LICENSE)
[![Nix Flake](https://img.shields.io/badge/nix-flake-blue?logo=nixos)](https://nixos.wiki/wiki/Flakes)

**A containerd shim that runs Nix flakes directly on Kubernetes — no container images required.**

[Pod spec](#pod-spec) •
[Installation](#installation-nixos) •
[Architecture](docs/architecture.md) •
[Contributing](CONTRIBUTING.md) •
[containix.dev](https://containix.dev)

</div>

---

Instead of building, pushing, and pulling OCI images, containix lets you reference a Nix flake in your Pod spec. The shim resolves the flake, fetches the closure from your binary cache, assembles a rootfs from Nix store paths, and hands off to runc. Pods start with exactly the packages they need — nothing more.

> **Status: experimental.** Validated end-to-end on NixOS aarch64 + k3s with ~91% test coverage across core packages. The API will change before v1; pin commits, not branches. Production use is not recommended yet.

## How it works

```
Pod spec (annotation: containix.dev/flake = "github:org/repo#app")
  → containerd invokes containix shim
    → shim resolves flake to store paths
      → bind-mounts Nix closure as container rootfs
        → runc executes the container
```

containix registers as a [containerd shim v2](https://github.com/containerd/containerd/blob/main/core/runtime/v2/README.md) runtime. A Kubernetes `RuntimeClass` routes opted-in pods to the shim. A near-empty stub image satisfies the CRI/OCI spec — the real filesystem comes from Nix.

See [docs/architecture.md](docs/architecture.md) for the deeper design.

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

## Node requirements

- containerd (default on EKS, GKE, most managed k8s; bundled in k3s).
- Nix daemon running on the node.
- Access to a binary cache (Cachix, Attic, or self-hosted) for cold-start performance.
- A reachable OCI stub image (CRI requires `image:` even though it isn't used).

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

## License

Copyright 2026 Ben Mask. Licensed under the [Apache License 2.0](./LICENSE). See [NOTICE](./NOTICE) for attribution.
