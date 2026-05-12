<p align="center">
  <img src="assets/logo.svg" alt="containix logo" width="600"/>
</p>

A containerd shim that runs Nix flakes directly on Kubernetes — no container images required.

Instead of building, pushing, and pulling OCI images, containix lets you reference a Nix flake in your Pod spec. The shim resolves the flake, fetches the closure from your binary cache, assembles a rootfs from Nix store paths, and hands off to runc. Pods start with exactly the packages they need — nothing more.

## How it works

```
Pod spec (annotation: containix.dev/flake = "github:org/repo#app")
  → containerd invokes containix shim
    → shim resolves flake to store paths
      → bind-mounts Nix closure as container rootfs
        → runc executes the container
```

containix registers as a [containerd shim v2](https://github.com/containerd/containerd/blob/main/core/runtime/v2/README.md) runtime. A Kubernetes `RuntimeClass` routes opted-in pods to the shim. A 49-byte stub image satisfies the CRI/OCI spec — the real filesystem comes from Nix.

## Pod spec

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: my-app
  annotations:
    containix.dev/flake: "github:myorg/myapp#default"
    containix.dev/entrypoint: "/bin/myapp"
spec:
  runtimeClassName: containix
  containers:
  - name: app
    image: "containix/empty:1.0.0"
```

## Node requirements

- containerd (standard on EKS, GKE, most managed k8s)
- Nix daemon running on the node
- Access to a binary cache (Cachix, Attic, or self-hosted)

## Project status

**Early development.** See [tutorials/](./tutorials/) for a step-by-step build journal.

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

## Tutorials

This project is built incrementally. Each tutorial builds on the last:

| # | Tutorial | What you'll build |
|---|----------|-------------------|
| 0 | [Foundations](./tutorials/00-foundations.md) | Understand containerd shims, CRI, and how containers actually start |
| 1 | [Hello Shim](./tutorials/01-hello-shim.md) | Minimal containerd shim v2 in Go that logs and delegates to runc |
| 2 | [Nix Rootfs](./tutorials/02-nix-rootfs.md) | Resolve a flake ref and assemble a rootfs from Nix store paths |
| 3 | [Local Integration](./tutorials/03-local-integration.md) | Run a Nix-backed container locally with containerd + your shim |
| 4 | [Kubernetes](./tutorials/04-kubernetes.md) | Deploy on a real cluster with RuntimeClass and a stub image |
| 5 | [Binary Cache](./tutorials/05-binary-cache.md) | Optimize cold-start with binary cache pre-warming and node-local stores |
| 6 | [Production Hardening](./tutorials/06-production-hardening.md) | Garbage collection, health checks, metrics, and failure modes |

## License

MIT
