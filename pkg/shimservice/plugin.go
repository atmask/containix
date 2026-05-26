//go:build linux

package shimservice

import (
	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/task"
	"github.com/containerd/containerd/v2/pkg/shim"
	"github.com/containerd/containerd/v2/pkg/shutdown"
	"github.com/containerd/containerd/v2/plugins"
	"github.com/containerd/plugin"
	"github.com/containerd/plugin/registry"
)

// Init called at package initialization
// It is used here to register the shim service into the
// global registry in the Data segement of memory
// Compare to: containerd/cmd/containerd-shim-runc-v2/task/plugin
func init() {
	registry.Register(&plugin.Registration{
		Type: plugins.TTRPCPlugin,
		ID:   "task",
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

			inner, err := task.NewTaskService(ic.Context, pp.(shim.Publisher), ss.(shutdown.Service))
			if err != nil {
				return nil, err
			}

			return &Service{TTRPCTaskService: inner}, nil
		},
	})
}
