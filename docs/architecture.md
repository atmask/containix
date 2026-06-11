# Architecture

How containix integrates with the Kubernetes + containerd stack at runtime.

## The stack

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

## What containix is, in containerd terms

containix is a **containerd shim v2 runtime**. A shim is the per-container process that supervises a container's lifecycle on behalf of containerd. The shim implements two distinct Go interfaces:

- **`shim.Shim`** — the bootstrap layer. `Name`/`Start`/`Stop`/`Info` methods. Invoked when containerd execs the shim binary with subcommands (`start`, `delete`). containix reuses the upstream runc shim's `manager.NewShimManager` for this — no customization needed.
- **`taskAPI.TTRPCTaskService`** — the per-container task layer. 17 methods including `Create`, `Start`, `Kill`, `Wait`, `Exec`. Served via TTRPC by the long-lived shim daemon process. **This is what containix customizes.**

Both interfaces are implemented by the same binary; they're reached through different code paths at different times. See [the containerd shim v2 documentation](https://github.com/containerd/containerd/blob/main/core/runtime/v2/README.md) for the full protocol.

## The Create handler

The interesting work happens in containix's `Create` override:

```
On Create:
  1. Read OCI bundle's config.json
  2. Extract containix.dev/flake and containix.dev/entrypoint annotations
  3. If no flake annotation OR sandbox container-type → passthrough to upstream runc shim
  4. Otherwise:
       a. Run `nix build <flakeRef>` to resolve the flake to a store path
       b. Compute the runtime closure via `nix-store -qR <storePath>`
       c. For each path in the closure, add a bind-mount entry to the spec:
            - source: /nix/store/<hash>-name on host
            - destination: /nix/store/<hash>-name in container rootfs
            - options: rbind, ro
       d. Resolve the entrypoint annotation against the store path:
            - "bin/hello"           → "<storePath>/bin/hello"
            - "/path/in/store"      → used as-is (absolute path escape hatch)
       e. Write the modified spec back to config.json
  5. Hand off to upstream runc shim's Create (locking, container map management,
     event emission, OOM monitoring, runc create invocation, response back to
     containerd)
```

Everything between "hand off" and "container running" is the standard runc shim path. Containix only modifies the spec; it doesn't replace the container lifecycle machinery.

## Why a shim and not a snapshotter

containerd's pluggable extension points include both **snapshotters** (which assemble the rootfs filesystem) and **shims** (which supervise the container process). Both can produce the same end result: a container running with Nix-sourced files. Tradeoffs:

| Aspect | Snapshotter approach | Shim approach (containix) |
|---|---|---|
| Integration point | Filesystem preparation | Container lifecycle |
| Reuses | Standard runc shim | Standard snapshotter |
| Modifies | Rootfs assembly | Spec generation, lifecycle hooks |
| Conceptual claim | "The image IS Nix paths" | "The runtime supervises a Nix-rooted container" |
| Lifecycle control | None — snapshotter is invoked only at prepare/cleanup | Full — every Task RPC is interceptable |

Containix's shim approach allows for future per-container customization (custom Wait semantics, GC roots tied to container lifecycle, etc.) at the cost of being a bigger surface area than the snapshotter route.

[nix-snapshotter](https://github.com/pdtpartners/nix-snapshotter) takes the snapshotter route — different design choice, more mature, used by Flox.

## The two TTRPC interfaces

Confusingly, containerd's shim involves two interfaces with "TTRPC" in their names:

- **`taskAPI.TTRPCTaskService`** (17 methods) — the task service contract; the Create/Start/Kill RPCs your wrapper serves.
- **`shim.TTRPCService`** (1 method: `RegisterTTRPC`) — the framework hook; how containerd discovers what to wire onto the TTRPC server.

Go's interface embedding promotes the 17 task methods automatically when you embed `TTRPCTaskService`, but it does NOT promote `RegisterTTRPC` (which lives on a different interface entirely). Containix's wrapper defines `RegisterTTRPC` explicitly, registering itself (the wrapper) as the TTRPC dispatch target so RPCs land on the overridden `Create`.

Forget this and the daemon fails at startup with `required that ttrpc service`. We hit this; it's a common gotcha for shim authors.

## What containix doesn't do

- **Doesn't replace the OCI image entirely.** A stub image still has to exist in your registry to satisfy CRI's required `image:` field. Its contents are unused — the shim overwrites the rootfs before runc creates the container.
- **Doesn't run on the API server side.** Everything happens on the node where the pod is scheduled.
- **Doesn't handle multi-container pods specially.** Each container gets its own `Create` call; the shim treats them independently.
- **Doesn't manage the Nix store.** Garbage collection, binary cache configuration, and Nix daemon lifecycle are the operator's responsibility.

## Wire-protocol notes

containerd ↔ shim uses **TTRPC**, not gRPC. Same protobuf message format as gRPC but a slimmer wire framing for low-overhead local-socket communication. Don't confuse with kubelet ↔ containerd, which IS gRPC (CRI).

The shim's containerd-library version must match the daemon's version on the target node. The v2.2 ↔ v2.3 bootstrap protocol is incompatible — a v2.3-built shim talking to a v2.2 daemon fails with `unsupported protocol:Yunix` (a protobuf framing byte misread as text). Pin to the oldest containerd version you intend to support.
