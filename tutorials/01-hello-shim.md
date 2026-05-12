# Tutorial 1: Hello Shim

Build a minimal containerd shim v2 in Go that does nothing except log and delegate to runc. This is the skeleton that all future tutorials build on.

## Goal

A working shim binary (`containerd-shim-containix-v1`) that:
- containerd can discover and spawn
- Passes through all operations to runc
- Logs every method call so you can trace the lifecycle

## Prerequisites

- Go 1.22+
- A Linux machine (or VM) with containerd installed
- Root access (containerd requires it)

## Project structure

```
containix/
├── cmd/
│   └── containerd-shim-containix-v1/
│       └── main.go
├── pkg/
│   └── shim/
│       └── service.go
├── go.mod
└── go.sum
```

## Step 1: Initialize the Go module

```bash
cd containix
go mod init github.com/atmask/containix
```

## Step 2: The shim entrypoint

The shim binary is spawned by containerd. The `containerd/runtime/v2/shim` package handles the gRPC server setup — you just provide your service implementation.

```go
// cmd/containerd-shim-containix-v1/main.go
package main

import (
	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/process"
	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/runc"
	"github.com/containerd/containerd/v2/pkg/shim"

	"github.com/atmask/containix/pkg/shimservice"
)

func main() {
	shim.Run(
		"io.containerd.containix.v1",
		shimservice.New,
	)
}
```

## Step 3: The shim service

For now, this wraps the standard runc service with logging. In later tutorials, we'll intercept `Create` to inject the Nix rootfs.

```go
// pkg/shimservice/service.go
package shimservice

import (
	"context"
	"log"

	taskAPI "github.com/containerd/containerd/v2/api/runtime/task/v3"
	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/runc"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.TTRuncShimV2,
		ID:   "containix",
		Requires: []plugin.Type{
			plugins.InternalPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (interface{}, error) {
			return New(ic.Context, ic.Properties)
		},
	})
}

// Service wraps the runc shim service with logging and
// will eventually intercept Create to inject Nix rootfs.
type Service struct {
	inner *runc.Service
}

func New(ctx context.Context, config shim.Config) (shim.Shim, error) {
	inner, err := runc.New(ctx, config)
	if err != nil {
		return nil, err
	}
	return &Service{inner: inner.(*runc.Service)}, nil
}

func (s *Service) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	log.Printf("[containix] Create called: id=%s bundle=%s", req.ID, req.Bundle)

	// TODO (Tutorial 2): intercept here to resolve flake and build Nix rootfs

	return s.inner.Create(ctx, req)
}

func (s *Service) Start(ctx context.Context, req *taskAPI.StartRequest) (*taskAPI.StartResponse, error) {
	log.Printf("[containix] Start called: id=%s", req.ID)
	return s.inner.Start(ctx, req)
}

func (s *Service) Delete(ctx context.Context, req *taskAPI.DeleteRequest) (*taskAPI.DeleteResponse, error) {
	log.Printf("[containix] Delete called: id=%s", req.ID)
	return s.inner.Delete(ctx, req)
}

func (s *Service) State(ctx context.Context, req *taskAPI.StateRequest) (*taskAPI.StateResponse, error) {
	return s.inner.State(ctx, req)
}

func (s *Service) Kill(ctx context.Context, req *taskAPI.KillRequest) (*emptypb.Empty, error) {
	log.Printf("[containix] Kill called: id=%s signal=%d", req.ID, req.Signal)
	return s.inner.Kill(ctx, req)
}

// Forward remaining methods to inner service.
// See the full interface in containerd/api/runtime/task/v3/shim.proto
// Methods: Pids, Pause, Resume, Checkpoint, Update, Wait, Stats, Connect, Shutdown
```

> **Note**: The exact API surface depends on your containerd version. The snippet above is illustrative — you'll need to implement the full `shim.Shim` interface. Start by looking at what `runc.Service` implements and wrapping each method.

## Step 4: Build

```bash
go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
```

## Step 5: Install and test

```bash
# Copy shim to PATH where containerd can find it
sudo cp bin/containerd-shim-containix-v1 /usr/local/bin/

# Configure containerd to know about the runtime
# Add to /etc/containerd/config.toml:
#
# [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
#   runtime_type = "io.containerd.containix.v1"

# Restart containerd
sudo systemctl restart containerd

# Test with ctr (not Kubernetes yet)
sudo ctr run \
  --rm \
  --runtime io.containerd.containix.v1 \
  docker.io/library/alpine:latest \
  test-containix \
  echo "hello from containix"
```

If you see `hello from containix` in the output and `[containix] Create called` in the containerd logs, it works.

## Step 6: Verify the shim lifecycle

```bash
# Watch shim processes while running a container
watch 'ps aux | grep containerd-shim-containix'

# Check containerd logs for your log lines
sudo journalctl -u containerd -f | grep containix
```

## What you've built

A containerd shim that:
- containerd discovers by binary name convention
- Logs every lifecycle event
- Delegates all actual work to runc
- Can be selected per-container via `--runtime`

This is the foundation. The shim currently does nothing different from the default runc shim — it's a transparent passthrough with logging. In the next tutorial, we'll intercept `Create` to replace the image rootfs with a Nix closure.

## Gotchas

- **The shim must be statically linked** or have all its library dependencies available on the node. Consider `CGO_ENABLED=0 go build`.
- **containerd spawns the shim as a child process** then reparents it. Don't be surprised when the parent PID changes.
- **Logs go to containerd's log output**, not stdout. Use `journalctl -u containerd` to see them.
- **The shim binary name must match exactly**: `containerd-shim-containix-v1`. If containerd can't find it on `$PATH`, you get a cryptic "not found" error.

## Next

[Tutorial 2: Nix Rootfs](./02-nix-rootfs.md) — resolve a flake ref and assemble a rootfs from Nix store paths.
