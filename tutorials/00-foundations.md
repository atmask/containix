# Tutorial 0: Foundations

Before writing any code, you need a mental model of how containers actually start on a Kubernetes node. This tutorial is all reading and experimenting — no containix code yet.

## What happens when a Pod starts

```
kubectl apply → API server → scheduler → kubelet → CRI → containerd → shim → runc
```

The kubelet doesn't run containers. It speaks CRI (Container Runtime Interface) to containerd, which manages the lifecycle. But containerd doesn't run containers either — it delegates to a **shim**.

## The containerd shim

A shim is a small binary that containerd spawns per-container (or per-pod sandbox). It:

1. Receives a `CreateTask` request with an OCI bundle (rootfs + config.json)
2. Prepares the container filesystem
3. Calls an OCI runtime (runc/crun) to actually create the namespaced process
4. Stays alive as the container's parent process (handles signals, exit status, stdio)

The shim is containerd's extension point. By writing a custom shim, we can intercept step 1-2 and replace the OCI image rootfs with a Nix closure.

The shim exists because containerd is a long-lived daemon and restarts should not impact running containers. The shim intentionally detaches itself from containerd in the process tree to avoid being impacted in this scenario.

Detaching from the process tree involves a double fork of the shim. The shim process initiated by containerd forks itself and then immediately exits. This orphans the new process that is then re-parented by PID 1. Verify this on any containerd node with `pstree -p 1 | grep shim` — the shim processes are children of init, not of containerd.

### One shim per pod sandbox

A subtle but important point: shim v2 runs **one shim per pod sandbox**, not one shim per container. A Kubernetes pod has a "sandbox" container (the `pause` binary) that holds the pod's shared namespaces, plus N application containers that join those namespaces. All of them are supervised by the same shim process.

The shim maintains an in-memory `containers map[string]*runc.Container` keyed by container ID. Counting shim processes on a node tells you the number of pod sandboxes (≈ pods), not the number of containers.

This matters for containix: the shim's `Create` will be called multiple times per pod — first for the sandbox, then once per app container. We only want to do Nix rootfs work for the app containers, identified by the annotation `io.kubernetes.cri.container-type: container`. Sandbox containers (`container-type: sandbox`) just run the upstream pause image and pass through to runc unchanged.

### Shim architecture — two layers

A containerd v2 shim binary has two distinct layers:

**Bootstrap layer** — the binary implements the `shim.Shim` interface (`Name`/`Start`/`Stop`/`Info`). containerd invokes this to spawn or stop the shim daemon process for a pod sandbox.

**Task service layer** — once spawned, the shim daemon serves a TTRPC API implementing `TTRPCTaskService`. This is the per-container/per-exec lifecycle: `Create`, `Start`, `Kill`, `Wait`, etc. containerd opens a TTRPC connection to this service over the daemon's local socket.

**TTRPC, not gRPC.** TTRPC uses the same protobuf message format as gRPC but strips HTTP/2 framing — dramatically lower binary size and per-process memory overhead, which matters when there can be hundreds of shim processes on a node. The kubelet↔containerd boundary is gRPC; the containerd↔shim boundary is TTRPC. They're easy to conflate but they're different protocols.

The shim daemon's job is mostly the Task service layer — that's where the interesting work happens:

| Method     | When it's called   | What it does                                   |
| ---------- | ------------------ | ---------------------------------------------- |
| `Create`   | Container creation | Prepare rootfs, runc create (paused init proc) |
| `Start`    | After Create       | runc start — exec the user's command           |
| `Exec`     | `kubectl exec`     | Inject a new process into the container        |
| `Kill`     | Termination        | Send a signal to the container process         |
| `Wait`     | Always running     | Block until the container exits                |
| `Delete`   | Cleanup            | Remove container state                         |
| `State`    | Health/inspect     | Report container state                         |
| `Stats`    | Metrics            | Report resource usage                          |
| `Shutdown` | Pod-sandbox gone   | Shim daemon exits                              |

Full proto definition: [containerd/api/runtime/task/v3/shim.proto](https://github.com/containerd/containerd/blob/main/api/runtime/task/v3/shim.proto)

For containix, the only method we need to customize is **`Create`** — that's where the rootfs gets assembled. Every other method we forward unchanged to the runc shim's existing implementation via Go interface embedding.

### Shim binary naming

containerd discovers shims by binary name convention:

```
containerd-shim-<name>-<version>
```

For containix, this will be:

```
containerd-shim-containix-v1
```

containerd finds this on `$PATH` and spawns it.

## RuntimeClass

A Kubernetes RuntimeClass tells the kubelet which shim to use:

```yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: containix
handler: containix # maps to containerd-shim-containix-v1
```

Pods opt in with `spec.runtimeClassName: containix`. Pods without it use the default runc shim — containix is purely additive.

## The stub image trick

Kubernetes requires an `image` field in the container spec. The CRI won't accept a Pod without one. But we don't actually need the image contents — the shim will replace the rootfs.

The solution: a minimal OCI image that satisfies the spec. We'll build our own:

```nix
# A minimal OCI image with a single empty layer
pkgs.dockerTools.buildImage {
  name = "containix/empty";
  tag = "1.0.0";
}
```

Or even simpler — a `FROM scratch` Dockerfile with a single empty file.

## Exercises

Before moving to Tutorial 1, do these to build intuition:

### 1. Watch a container start

On a Linux machine (or VM) with containerd:

```bash
# Watch containerd's shim processes
watch 'ps aux | grep containerd-shim'

# In another terminal, start a container
sudo ctr run --rm docker.io/library/alpine:latest test echo hello

# Observe the shim spawn and exit
```

**On a k3s node**, the bundled containerd lives at a non-standard path. Substitute:

```bash
sudo k3s ctr -n k8s.io containers ls          # list containers
sudo k3s crictl pods                            # list pod sandboxes (= shim count)
sudo ps aux | grep containerd-shim              # see the shim processes
sudo pstree -p 1 | grep shim                    # verify they're children of PID 1
```

The `-n k8s.io` is a **containerd namespace** — different from Kubernetes namespaces. All kubelet-managed workloads live in this namespace. Without the flag you'll see empty results.

The shim argv tells you exactly what's running: `-namespace k8s.io -id <container-id> -address /run/k3s/containerd/containerd.sock`. The `address` is containerd's socket (so the shim can call back), not the shim's own socket.

### 2. Read the runc shim source

The reference implementation is ~1500 lines of Go:

- [containerd-shim-runc-v2](https://github.com/containerd/containerd/tree/main/cmd/containerd-shim-runc-v2)

The relevant entry points:

- [`main.go`](https://github.com/containerd/containerd/blob/v2.3.1/cmd/containerd-shim-runc-v2/main.go) — 10 lines. Calls `shim.RunShim(ctx, manager.NewShimManager("io.containerd.runc.v2"))` and blank-imports the task plugin. This is the model containix follows.
- [`manager/manager_linux.go`](https://github.com/containerd/containerd/blob/v2.3.1/cmd/containerd-shim-runc-v2/manager/manager_linux.go) — implements the bootstrap layer (`shim.Shim` interface). We **reuse this unchanged** for containix.
- [`task/plugin/plugin_linux.go`](https://github.com/containerd/containerd/blob/v2.3.1/cmd/containerd-shim-runc-v2/task/plugin/plugin_linux.go) — registers the Task service plugin via `init()`. containix writes its own version of this to register a wrapping task service.
- [`task/service.go`](https://github.com/containerd/containerd/blob/v2.3.1/cmd/containerd-shim-runc-v2/task/service.go) — the Task service implementation. Focus on the `Create` method. The wrapping pattern containix uses leaves this unchanged but intercepts `Create` to do Nix rootfs prep before calling through.

### 3. Inspect a Nix closure

```bash
# Build something simple
nix build nixpkgs#hello

# See the output path
readlink result

# See the full closure (all dependencies)
nix-store -qR result

# Count the total size
nix path-info -rSh result
```

This closure is what containix will mount as the container rootfs. Every path in the closure gets bind-mounted into the container.

### 4. Inspect an OCI bundle

```bash
# Pull and unpack an image
sudo ctr image pull docker.io/library/alpine:latest
sudo ctr image mount docker.io/library/alpine:latest /tmp/alpine-rootfs

# Look at the rootfs
ls /tmp/alpine-rootfs

# Compare with a Nix closure
ls $(nix build nixpkgs#hello --print-out-paths)/bin/

# Cleanup
sudo ctr image unmount /tmp/alpine-rootfs
```

## Key takeaways

- Containerd shims are the right extension point — they control rootfs preparation
- The shim v2 API is small (~10 methods) and well-documented
- RuntimeClass makes the shim opt-in per pod
- A stub image satisfies the CRI without providing real content
- A Nix closure contains everything a process needs to run

## Next

[Tutorial 1: Hello Shim](./01-hello-shim.md) — write a minimal shim that logs and delegates to runc.
