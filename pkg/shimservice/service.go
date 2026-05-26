package shimservice

import (
	"context"
	"log"

	taskAPI "github.com/containerd/containerd/api/runtime/task/v3"
)

// Wrap the upstream task Service
// Anonmyous embedding promotes the implemented interface methods to this struct where those methods are not overriden
type Service struct {
	taskAPI.TTRPCTaskService
}

// Override the task creation method
func (s *Service) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	log.Printf("[containix] Create called: id=%s, bundle=%s", req.ID, req.Bundle)
	return s.TTRPCTaskService.Create(ctx, req)
}
