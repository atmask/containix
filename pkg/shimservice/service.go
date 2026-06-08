package shimservice

import (
	"context"
	"fmt"
	"log"

	"github.com/atmask/containix/pkg/nix"
	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
	"github.com/containerd/ttrpc"
)

// Wrap the upstream task Service.
// Anonymous embedding promotes the implemented interface methods to this struct
// where those methods are not overridden.
type Service struct {
	taskAPI.TTRPCTaskService
}

// RegisterTTRPC is required by containerd's shim framework — it type-asserts
// each loaded plugin instance to `shim.TTRPCService` to know what to wire onto
// the TTRPC server. The embedded `TTRPCTaskService` interface does NOT include
// RegisterTTRPC, so without this method our wrapper wouldn't satisfy the
// `shim.TTRPCService` interface and the framework would error with
// "required that ttrpc service".
//
// We register *Service (not the embedded inner) so containerd's RPCs land on
// our wrapper, allowing our Create override to fire.
func (s *Service) RegisterTTRPC(server *ttrpc.Server) error {
	taskAPI.RegisterTTRPCTaskService(server, s)
	return nil
}

// Override the task creation method.
func (s *Service) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	log.Printf("[containix] Create called: id=%s, bundle=%s", req.ID, req.Bundle)

	// Read Annotation. Returns nil if this isn't a containix container,
	// or if it's the pod sandbox (container-type=sandbox) - both pass through
	cfg, err := nix.FromBundle(req.Bundle)
	if err != nil {
		log.Printf("[containix] error reading annotations: %v", err)
		return nil, err
	}

	if cfg != nil {
		log.Printf("[containix] Nix flake detected: %s", cfg.FlakeRef)

		storePath, err := nix.Build(cfg.FlakeRef)
		if err != nil {
			return nil, fmt.Errorf("nix build: %w", err)
		}

		log.Printf("[containix] store path: %s", storePath)

		closure, err := nix.Closure(storePath)
		if err != nil {
			return nil, fmt.Errorf("nix closure: %w", err)
		}
		log.Printf("[containix] closure: %d paths", len(closure))

		if err := nix.PrepareRootfs(req.Bundle, storePath, cfg, closure); err != nil {
			return nil, fmt.Errorf("preparing rootfs: %w", err)
		}
	}

	return s.TTRPCTaskService.Create(ctx, req)
}

// NOTE: Shims has this two interface relationship with containerd.
// Firstly, the shim.Shim interface. This is for the ephmeral bootstrapping
// an orphaned and long-lived TTRPC service implementing a second interface:
// taskAPI.TTRPCTaskService. The ephmeral process of the shim interface
// createss the daemon TTRPC service and exists leading to the
// reparenting/daemonization of under pid 1.
