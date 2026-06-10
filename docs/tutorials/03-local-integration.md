# Tutorial 3: Local Integration

Run a Nix-backed container end-to-end using containerd and your shim — no Kubernetes yet. This validates the full lifecycle before adding cluster complexity.

## Goal

- Run a Nix flake as a container via `ctr` with the containix runtime
- Run a multi-process flake (e.g., a web server)
- Validate container lifecycle: create, start, exec, stop, delete
- Debug common failure modes

## Prerequisites

- Everything from Tutorials 0-2 built and installed
- A Linux machine with containerd running
- Nix daemon running (`nix doctor` should pass)

## Step 1: Build and install the shim

```bash
CGO_ENABLED=0 go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
sudo cp bin/containerd-shim-containix-v1 /usr/local/bin/
sudo systemctl restart containerd
```

## Step 2: Create a test flake with a long-running process

Hello world exits immediately. Let's use a simple HTTP server to test the full lifecycle.

```nix
# test-server/flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { nixpkgs, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      packages.${system}.default = pkgs.writeShellScriptBin "serve" ''
        ${pkgs.python3}/bin/python3 -m http.server 8080
      '';
    };
}
```

Build it and note the output path:

```bash
cd test-server
nix build
readlink result
# /nix/store/xxx-serve-1.0/bin/serve
```

## Step 3: Pull the stub image

containerd needs an image to satisfy the CRI. Pull a minimal one:

```bash
sudo ctr image pull docker.io/library/alpine:latest
```

We'll use alpine as the stub for now. In production, you'd use a purpose-built empty image.

## Step 4: Run the container

```bash
sudo ctr run \
  --detach \
  --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="path:$(pwd)/test-server#default" \
  --label containix.dev/entrypoint="/nix/store/xxx-serve-1.0/bin/serve" \
  docker.io/library/alpine:latest \
  containix-test-server
```

Replace the store path with the actual path from `nix build --print-out-paths`.

## Step 5: Validate

```bash
# Check the container is running
sudo ctr task list

# Check the shim process
ps aux | grep containerd-shim-containix

# Check containerd logs for containix trace
sudo journalctl -u containerd --since "5 minutes ago" | grep containix

# If the server is running, test it
# (you'll need to find the container's IP or use network namespace tools)
sudo ctr task exec --exec-id test containix-test-server cat /proc/1/cmdline
```

## Step 6: Test lifecycle operations

```bash
# Pause
sudo ctr task pause containix-test-server

# Resume
sudo ctr task resume containix-test-server

# Stop
sudo ctr task kill containix-test-server --signal SIGTERM

# Delete
sudo ctr task delete containix-test-server
sudo ctr container delete containix-test-server
```

## Debugging

### "exec failed: no such file or directory"

The entrypoint path is wrong or the Nix store path wasn't mounted. Check:

```bash
# Inspect the OCI spec that was generated
sudo cat /run/containerd/io.containerd.runtime.v2.task/default/containix-test-server/config.json | jq '.mounts[] | select(.destination | startswith("/nix"))'
```

### "nix build failed"

The Nix daemon might not be accessible from the shim. The shim runs as root — verify:

```bash
sudo nix build path:$(pwd)/test-server#default --print-out-paths
```

### Container starts but process crashes

The closure might be missing runtime dependencies. Check:

```bash
# Get the closure
nix-store -qR $(nix build path:$(pwd)/test-server#default --print-out-paths)

# Look for missing shared libs
ldd $(nix build path:$(pwd)/test-server#default --print-out-paths)/bin/serve
```

### Shim logs are empty

Make sure you rebuilt and reinstalled the binary:

```bash
which containerd-shim-containix-v1
# should be /usr/local/bin/containerd-shim-containix-v1

containerd-shim-containix-v1 --version
```

## What you've validated

- The shim correctly intercepts `Create` and injects Nix store mounts
- containerd can manage the full lifecycle of a Nix-backed container
- The container process runs with real Nix packages
- Lifecycle operations (pause, resume, kill, delete) work through the shim

## Next

[Tutorial 4: Kubernetes](./04-kubernetes.md) — deploy on a real cluster with RuntimeClass.
