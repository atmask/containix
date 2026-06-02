# Tutorial 1: Hello Shim

Build a minimal containerd shim v2 in Go that does nothing except log and delegate to runc. This is the skeleton that all future tutorials build on.

## Goal

A working shim binary (`containerd-shim-containix-v1`) that:

- containerd can discover and spawn
- Passes through all operations to runc
- Logs every method call so you can trace the lifecycle

## Prerequisites

- Go 1.22+ (exact minimum depends on which containerd version you target — see "Pin to your target containerd version" below)
- A Linux machine (or VM) with containerd installed
- Root access (containerd requires it)

## Pin to your target containerd version

> **Important**: the shim is part of a wire protocol contract with containerd. Build your shim against the **same containerd library version as the daemon running on your target node**, or the bootstrap handshake will fail with confusing errors like `unsupported protocol:Yunix` (a sign the daemon is parsing your shim's response with the wrong protocol version).

Find your target daemon's version:

```bash
# on the target machine (or in your dev VM if it matches the target)
containerd --version
# e.g. "containerd github.com/containerd/containerd/v2 v2.2.3"
```

Then pin the shim's library dep to match:

```bash
# replace v2.2.3 with whatever your daemon runs
go get github.com/containerd/containerd/v2@v2.2.3
```

You also need a compatible `containerd/api` version. Find the right one in containerd's go.mod for that release:

```bash
curl -s https://raw.githubusercontent.com/containerd/containerd/v2.2.3/go.mod | grep containerd/api
# e.g. "github.com/containerd/containerd/api v1.10.0"

go get github.com/containerd/containerd/api@v1.10.0
go mod tidy
```

If your shim ever needs to support multiple target containerd versions, build separate binaries per target.

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
	shim.Run(context.Background(), manager.NewShimManager("io.containerd.containix.v1"))
}
```

Key points:

- **`shim.Run` vs `shim.RunShim`.** Same call shape, signature is `(ctx, Shim, opts...)`. The function was renamed in containerd v2.3 — `Run` is the current name in v2.2 and earlier, `RunShim` is the v2.3+ name with `Run` deprecated as an alias. Use whichever your target containerd's library exports. If you see `undefined: shim.RunShim` at build time, you're on v2.2 and need `shim.Run`. If you see `shim.Run is deprecated, use RunShim`, you're on v2.3+.
- **`manager.NewShimManager` from the upstream runc shim** handles the bootstrap layer (`Shim` interface). Pass your runtime ID; the manager handles everything else (process spawning, socket plumbing, signal handling).
- The blank import `_ "github.com/atmask/containix/pkg/shimservice"` triggers the package's `init()` function, which registers our task plugin in containerd's plugin registry. `shim.Run` looks the plugin up by ID at runtime.

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
	"github.com/containerd/ttrpc"
)

// Service wraps the upstream runc task service. Methods not explicitly
// overridden are forwarded via interface embedding — TTRPCTaskService's
// 17 methods are all promoted from the embedded field.
type Service struct {
	taskAPI.TTRPCTaskService
}

// RegisterTTRPC is required by containerd's shim framework. The framework
// type-asserts every loaded plugin to `shim.TTRPCService` (which requires
// this single method) to know what to wire onto the TTRPC server.
//
// The embedded `taskAPI.TTRPCTaskService` interface does NOT declare
// RegisterTTRPC, so interface embedding alone doesn't satisfy
// `shim.TTRPCService`. Without this explicit method, the framework errors
// at daemon startup with: "required that ttrpc service".
//
// We register `s` (our wrapper) so RPCs land on our Create override,
// not on the inner upstream service directly.
func (s *Service) RegisterTTRPC(server *ttrpc.Server) error {
	taskAPI.RegisterTTRPCTaskService(server, s)
	return nil
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

### Why RegisterTTRPC has to be defined explicitly (not promoted)

Interface embedding promotes only the methods **declared by the embedded interface**, not the methods of whatever concrete value happens to fill it at runtime.

The upstream `task.NewTaskService` returns a concrete struct that implements *both* `taskAPI.TTRPCTaskService` (17 methods) and `shim.TTRPCService` (the single-method `RegisterTTRPC`). But its return type is declared as `taskAPI.TTRPCTaskService` — the narrower interface. When we embed that interface, only the 17 declared methods are visible to the Go compiler; `RegisterTTRPC` is hidden even though the underlying value has it.

The shim framework walks every registered plugin and type-asserts to `shim.TTRPCService`. If our wrapper doesn't satisfy that interface, the assertion fails silently and our task plugin is dropped — leading to "required that ttrpc service" when the framework checks that at least one TTRPC service registered.

The fix is to define `RegisterTTRPC` on our wrapper ourselves, registering `s` (the wrapper) as the task service on the TTRPC server. Now:
1. `*Service` satisfies `shim.TTRPCService` (compile-time evidence: this method).
2. Incoming RPCs land on our wrapper, so our `Create` override fires. (If we forwarded to the inner's `RegisterTTRPC` instead, the inner would be wired up directly and our override would be bypassed.)

## Step 5: Add dependencies and build

If you haven't already pinned versions in the "Pin to your target containerd version" section:

```bash
go get github.com/containerd/containerd/v2@<your-target-version>
go get github.com/containerd/containerd/api@<matching-api-version>
go mod tidy
```

Then build. From your dev machine, cross-compile to match the target node:

```bash
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
```

Substitute `GOARCH=amd64` for x86_64 nodes. `CGO_ENABLED=0` produces a statically linked binary, avoiding glibc version mismatches between your build host and the node.

If building directly on the target machine (same OS+arch), drop the GOOS/GOARCH and let `go build` infer them.

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

- **Library version must match the daemon.** Shim ↔ containerd is a versioned wire protocol. A shim built against containerd v2.3 talking to a v2.2 daemon fails with "unsupported protocol:Yunix" (a sign that the daemon is parsing a protobuf bootstrap response with the old plain-text parser). Always pin to the target daemon's version.
- **`shim.Run` vs `shim.RunShim` depends on version.** v2.2 only exports `Run`. v2.3+ exports both, but `Run` is deprecated there. Match what your pinned containerd library exports.
- **Without `RegisterTTRPC`, the daemon fails to start** with `"required that ttrpc service"`. The shim framework type-asserts every loaded plugin to `shim.TTRPCService`; if your wrapper doesn't satisfy that, no TTRPC services register and the daemon exits.
- **The shim must be statically linked** or have all its library dependencies available on the node. `CGO_ENABLED=0 go build` produces a static binary.
- **containerd spawns the shim, which then reparents itself to PID 1.** Don't be surprised that the shim isn't a child of containerd in `pstree`. That's the [[double-fork-daemonization|double-fork]] equivalent at work — it's what lets containerd restart without killing containers.
- **Logs go to containerd's log output**, not stdout. Use `journalctl -u containerd` (or `journalctl -u k3s` on k3s) to see them.
- **The shim binary name must match exactly**: `containerd-shim-containix-v1`. containerd resolves `runtime_type = "io.containerd.containix.v1"` to `containerd-shim-containix-v1` on `$PATH`. If it can't find the binary, you get a cryptic "not found" error.
- **Don't blank-import both your task plugin and the upstream one.** They register under the same plugin ID (`"task"`) and the second registration will fail at runtime. Only import your `pkg/shimservice/`.

## Debugging hints

When the smoke test fails, the symptoms map to specific bugs:

| Symptom | Likely cause |
|---|---|
| `ctr: image "...": not found` | Image not pulled. Run `sudo ctr image pull <image>` first. |
| `failed to create TTRPC connection: unsupported protocol:Yunix` | containerd library version doesn't match the daemon's bootstrap protocol. Pin to the daemon's version. |
| `ttrpc: closed` + `required that ttrpc service` in logs | Your wrapper doesn't satisfy `shim.TTRPCService`. Add the `RegisterTTRPC` method. |
| `undefined: shim.RunShim` at build time | You're on containerd v2.2 or earlier; use `shim.Run`. |
| `cannot find package "io.containerd.runc.v2"` or similar | The wrong `containerd/api` version; check what containerd's go.mod for your version pins. |

## Next

[Tutorial 2: Nix Rootfs](./02-nix-rootfs.md) — resolve a flake ref and assemble a rootfs from Nix store paths.
