<p align="center">
  <img src="assets/logo.svg" alt="containix logo" width="600"/>
</p>

<div align="center">

[![CI](https://github.com/atmask/containix/actions/workflows/ci.yml/badge.svg)](https://github.com/atmask/containix/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/containix.dev.svg)](https://pkg.go.dev/containix.dev)
[![License: Apache 2.0](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](./LICENSE)
[![Nix Flake](https://img.shields.io/badge/nix-flake-blue?logo=nixos)](https://nixos.wiki/wiki/Flakes)

**A containerd shim that runs Nix flakes directly on Kubernetes — no container images required.**

[Pod spec](#pod-spec) •
[Kubernetes guide](docs/kubernetes.md) •
[Installation](#installation-nixos) •
[Architecture](docs/architecture.md) •
[Contributing](CONTRIBUTING.md) •
[containix.dev](https://containix.dev)

</div>

---

Instead of building, pushing, and pulling OCI images, containix lets you reference a Nix flake in your Pod spec. The shim resolves the flake, fetches the closure from your binary cache, assembles a rootfs from Nix store paths, and hands off to runc. Pods start with exactly the packages they need — nothing more.

> **Status: experimental.** Validated end-to-end on NixOS aarch64 + k3s; the core Nix resolution logic (`pkg/nix`) is ~93% test-covered. The API will change before v1 — pin commits, not branches. Production use is not recommended yet. See [Limitations](#limitations-v010).

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
    # The flake is the single source of truth. If its default package declares
    # meta.mainProgram, this is the only annotation you need.
    containix.dev/flake: "github:myorg/myapp"
spec:
  runtimeClassName: containix
  containers:
    - name: app
      image: "ghcr.io/atmask/containix-empty:0.1.0"
```

containix resolves the entrypoint from the flake itself: a bare ref builds
`packages.<system>.default`, and its [`meta.mainProgram`](https://nixos.org/manual/nixpkgs/stable/#var-meta-mainProgram)
becomes the command. Override it with `containix.dev/entrypoint` only when you
need a different binary — see below.

### Annotations

| Annotation | Required | Default | Description |
|---|---|---|---|
| `containix.dev/flake` | yes | — | Flake reference to resolve. Any [flake URL Nix accepts](https://nix.dev/manual/nix/stable/command-ref/new-cli/nix3-flake#flake-references) (`github:org/repo#default`, `nixpkgs#hello`, `path:/local#out`). |
| `containix.dev/entrypoint` | no | `meta.mainProgram` | Overrides the executable. Relative paths resolve against the flake's store path (`bin/myapp` → `/nix/store/<hash>-myapp/bin/myapp`); absolute paths (`/...`) are used as-is. **If omitted, containix uses the default package's `meta.mainProgram`.** If neither is set, container creation fails with a clear error. |
| `containix.dev/timeout` | no | `2m` | Maximum time for `nix build` + closure resolution. [Go duration format](https://pkg.go.dev/time#ParseDuration) (`30s`, `5m`, `1h`). Capped at `30m`. Invalid values fall back to the default. |

## Node requirements

- containerd **2.3 or newer** (default on EKS, GKE, most managed k8s; bundled in k3s). The shim is built against the containerd 2.4 library, and 2.2 daemons reject its bootstrap response with `unsupported protocol`.
- Nix daemon running on the node.
- Access to a binary cache (Cachix, Attic, or self-hosted) for cold-start performance.
- A reachable OCI stub image (CRI requires `image:` even though it isn't used).

## Installation (NixOS)

> This is the NixOS quickstart. For the full deployment guide — non-NixOS nodes,
> managed clusters (EKS/GKE), k3s, the `RuntimeClass`, and troubleshooting — see
> **[Running containix on Kubernetes](docs/kubernetes.md)**.

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

The module installs the shim binary, configures containerd to register `runtime_type = "io.containerd.containix.v1"`, and ensures `runc` and `nix` are on containerd's PATH. After `nixos-rebuild switch`, smoke-test the node (`ctr` doesn't auto-pull, so pull the ignored stub image first):

```bash
# nixpkgs#hello declares meta.mainProgram = "hello", so no entrypoint annotation
# is needed — containix resolves and runs $out/bin/hello.
sudo ctr image pull docker.io/library/busybox:latest
sudo ctr run --rm \
  --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="nixpkgs#hello" \
  docker.io/library/busybox:latest containix-smoke-test
```

Non-NixOS deployments need to install the binary and configure containerd manually.

## Limitations (v0.1.0)

Experimental and intentionally minimal. Known gaps:

- **Self-contained binaries only.** Nix binaries resolve their interpreter and libraries to `/nix/store` paths, so they run without an FHS layout — but programs that shell out to `/bin/sh`, rely on `#!/usr/bin/env`, or expect `/usr` and `/lib` aren't supported yet.
- **No interactive tooling.** `kubectl exec` lands in a container without `sh`/`ls` on `PATH`.
- **Policy is Kubernetes' job.** containix mirrors runc's options rather than enforcing its own — use PodSecurity admission for non-root/UID policy and NetworkPolicy for egress. It does not validate or allowlist flake refs.
- **NixOS-first.** Non-NixOS nodes require installing the shim binary and configuring containerd manually.

## License

Copyright 2026 Ben Mask. Licensed under the [Apache License 2.0](./LICENSE). See [NOTICE](./NOTICE) for attribution.
