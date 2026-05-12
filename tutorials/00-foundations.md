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

### Shim v2 API

The shim implements a gRPC service defined by containerd. The key methods:

| Method | When it's called | What it does |
|--------|-----------------|--------------|
| `Create` | Pod starts | Prepare rootfs, create container |
| `Start` | After Create | Start the container process |
| `Delete` | Pod stops | Clean up |
| `State` | Health checks | Report container state |
| `Kill` | Pod termination | Send signal to process |

Full proto definition: [containerd/api/runtime/task/v3/shim.proto](https://github.com/containerd/containerd/blob/main/api/runtime/task/v3/shim.proto)

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
handler: containix  # maps to containerd-shim-containix-v1
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

### 2. Read the runc shim source

The reference implementation is ~1500 lines of Go:
- [containerd-shim-runc-v2](https://github.com/containerd/containerd/tree/main/cmd/containerd-shim-runc-v2)

Focus on `task/service.go` — specifically the `Create` and `Start` methods. This is what we'll customize.

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
