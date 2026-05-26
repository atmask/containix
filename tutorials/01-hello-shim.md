# Tutorial 1: Hello Shim

Build a minimal containerd shim v2 in Go that does nothing except log and delegate to runc. This is the skeleton that all future tutorials build on.

## Goal

A working shim binary (`containerd-shim-containix-v1`) that:

- containerd can discover and spawn
- Passes through all operations to runc
- Logs every method call so you can trace the lifecycle

## Prerequisites

- Go 1.26.3+ (required by containerd v2.3.x)
- A Linux machine (or VM) with containerd installed (k3s works fine)
- Root access (containerd requires it)

## Architecture recap

From Tutorial 0: a containerd v2 shim has two layers.

```
┌────────────────────────────────────────────────────┐
│ Bootstrap layer — shim.Shim interface              │
│   Name/Start/Stop/Info                              │
│   ← we REUSE upstream manager.NewShimManager        │
└──────────────────────┬─────────────────────────────┘
                       │ spawns the shim daemon
┌──────────────────────▼─────────────────────────────┐
│ Task service layer — TTRPCTaskService               │
│   Create/Start/Kill/Wait/Exec/...                   │
│   ← we WRAP upstream task.NewTaskService            │
│     and override only Create                        │
└────────────────────────────────────────────────────┘
```

The runc shim already has working implementations for both layers. We reuse the bootstrap layer verbatim and wrap the task layer to intercept `Create`.

## Project structure

```
containix/
├── cmd/
│   └── containerd-shim-containix-v1/
│       └── main.go
├── pkg/
│   └── shimservice/
│       ├── plugin.go      ← registers the task plugin via init()
│       └── service.go     ← the wrapping TTRPCTaskService
├── go.mod
└── go.sum
```

## Step 1: Initialize the Go module

```bash
cd containix
go mod init github.com/atmask/containix
```

## Step 2: The shim entrypoint

The entire `main.go`:

```go
//go:build linux

package main

import (
	"context"

	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/manager"
	"github.com/containerd/containerd/v2/pkg/shim"

	// Blank import registers our task plugin via init() in this package.
	// Do NOT also blank-import the upstream task/plugin — it would register
	// a conflicting "task" plugin and the second registration would fail.
	_ "github.com/atmask/containix/pkg/shimservice"
)

func main() {
	shim.RunShim(context.Background(), manager.NewShimManager("io.containerd.containix.v1"))
}
```

Two important details:

- **`shim.RunShim`**, not `shim.Run`. The latter is deprecated. The signature is `RunShim(ctx, shim, opts...)`.
- **`manager.NewShimManager` from the upstream runc shim** handles the bootstrap layer (`Shim` interface). Pass your runtime ID; the manager handles everything else (process spawning, socket plumbing, signal handling).
- The blank import `_ "github.com/atmask/containix/pkg/shimservice"` triggers the package's `init()` function, which registers our task plugin in containerd's plugin registry. `shim.RunShim` looks the plugin up by ID at runtime.

## Step 3: Register the task plugin

```go
// pkg/shimservice/plugin.go
package shimservice

import (
	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/task"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/pkg/shutdown"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.TTRPCPlugin,
		ID:   "task",
        // Defined in containerd/pkg/shim.go
		Requires: []plugin.Type{
			plugins.EventPlugin,
			plugins.InternalPlugin,
		},
		InitFn: func(ic *plugin.InitContext) (any, error) {
			pp, err := ic.GetByID(plugins.EventPlugin, "publisher")
			if err != nil {
				return nil, err
			}
			ss, err := ic.GetByID(plugins.InternalPlugin, "shutdown")
			if err != nil {
				return nil, err
			}

            // For now pass-through to the runc TTRPC task service
			inner, err := task.NewTaskService(ic.Context, pp.(shim.Publisher), ss.(shutdown.Service))
			if err != nil {
				return nil, err
			}

			return &Service{TTRPCTaskService: inner}, nil
		},
	})
}
```

This is nearly identical to the upstream task plugin — same plugin ID (`"task"`), same dependencies (`EventPlugin` for the event publisher, `InternalPlugin` for the shutdown service). The only change: we wrap the returned `task.NewTaskService` in our own `Service` struct.

## Step 4: The wrapping service

```go
// pkg/shimservice/service.go
package shimservice

import (
	"context"
	"log"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
)
``
// Service wraps the upstream runc task service. Methods not explicitly
// overridden are forwarded via interface embedding — TTRPCTaskService's
// 17 methods are all promoted from the embedded field.
type Service struct {
	taskAPI.TTRPCTaskService
}

// Create is the one method we intercept. For now, just log and forward.
// Tutorial 2 inserts Nix rootfs assembly here.
func (s *Service) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	log.Printf("[containix] Create called: id=%s bundle=%s", req.ID, req.Bundle)
	return s.TTRPCTaskService.Create(ctx, req)
}
```

### Why interface embedding works

`TTRPCTaskService` has 17 methods (`State`, `Create`, `Start`, `Delete`, `Pids`, `Pause`, `Resume`, `Checkpoint`, `Kill`, `Exec`, `ResizePty`, `CloseIO`, `Update`, `Wait`, `Stats`, `Connect`, `Shutdown`). Writing 17 forwarding functions by hand would be tedious and error-prone.

By embedding `taskAPI.TTRPCTaskService` as an anonymous field in our struct, **all 17 methods are promoted** automatically — `s.State(ctx, req)` forwards to `s.TTRPCTaskService.State(ctx, req)` for free. Any method we explicitly define on `*Service` (like `Create`) shadows the embedded one.

When you want to add interception for another method later, just define it on `*Service`. Until then, the embedded interface handles it.

> **Gotcha**: this only works because Go interfaces support embedding. The embedded field has the same name as the interface (`TTRPCTaskService`), so we reference it as `s.TTRPCTaskService.Create(...)` when forwarding.

## Step 5: Add dependencies and build

```bash
go get github.com/containerd/containerd/v2@latest
go mod tidy

CGO_ENABLED=0 go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
```

`CGO_ENABLED=0` produces a statically linked binary, avoiding glibc version mismatches between your build host and the node.

## Step 6: Install and configure containerd

```bash
sudo cp bin/containerd-shim-containix-v1 /usr/local/bin/
```

### On a standard containerd node

Edit `/etc/containerd/config.toml`:

```toml
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
```

```bash
sudo systemctl restart containerd
```

### On a k3s node

k3s generates `/var/lib/rancher/k3s/agent/etc/containerd/config.toml` on every restart, so direct edits get overwritten. Instead, create a template file:

```bash
# Start from the rendered config as a baseline
sudo cp /var/lib/rancher/k3s/agent/etc/containerd/config.toml \
        /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl

# Append the containix runtime block to the template
sudo tee -a /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl <<'EOF'

[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
EOF

sudo systemctl restart k3s
```

On next start, k3s renders `config.toml` from your `config.toml.tmpl` instead of regenerating from scratch.

## Step 7: Test with ctr

```bash
# Standard containerd
sudo ctr run \
  --rm \
  --runtime io.containerd.containix.v1 \
  docker.io/library/alpine:latest \
  test-containix \
  echo "hello from containix"

# k3s
sudo k3s ctr run \
  --rm \
  --runtime io.containerd.containix.v1 \
  docker.io/library/alpine:latest \
  test-containix \
  echo "hello from containix"
```

If you see `hello from containix` in the output and `[containix] Create called` in the containerd logs, it works.

## Step 8: Verify the shim lifecycle

```bash
# Watch shim processes while running a container
watch 'ps aux | grep containerd-shim-containix'

# Check containerd logs for your log lines
sudo journalctl -u containerd -f | grep containix
# or on k3s:
sudo journalctl -u k3s -f | grep containix
```

## What you've built

A containerd shim that:

- containerd discovers by binary name convention
- Logs every lifecycle event
- Delegates all actual work to runc
- Can be selected per-container via `--runtime`

This is the foundation. The shim currently does nothing different from the default runc shim — it's a transparent passthrough with logging. In the next tutorial, we'll intercept `Create` to replace the image rootfs with a Nix closure.

## Gotchas

- **The shim must be statically linked** or have all its library dependencies available on the node. `CGO_ENABLED=0 go build` produces a static binary.
- **containerd spawns the shim, which then reparents itself to PID 1.** Don't be surprised that the shim isn't a child of containerd in `pstree`. That's the [[double-fork-daemonization|double-fork]] equivalent at work — it's what lets containerd restart without killing containers.
- **Logs go to containerd's log output**, not stdout. Use `journalctl -u containerd` (or `journalctl -u k3s` on k3s) to see them.
- **The shim binary name must match exactly**: `containerd-shim-containix-v1`. containerd resolves `runtime_type = "io.containerd.containix.v1"` to `containerd-shim-containix-v1` on `$PATH`. If it can't find the binary, you get a cryptic "not found" error.
- **Don't blank-import both your task plugin and the upstream one.** They register under the same plugin ID (`"task"`) and the second registration will fail at runtime. Only import your `pkg/shimservice/`.
- **Go version matters.** containerd v2.3.x requires Go 1.26.3+. The toolchain directive in `go.mod` will get bumped automatically by `go get`.

## Next

[Tutorial 2: Nix Rootfs](./02-nix-rootfs.md) — resolve a flake ref and assemble a rootfs from Nix store paths.
