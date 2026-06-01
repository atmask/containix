package shimservice

import (
	"context"
	"log"

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
	return s.TTRPCTaskService.Create(ctx, req)
}
