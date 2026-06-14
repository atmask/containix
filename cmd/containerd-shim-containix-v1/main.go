//go:build linux

package main

import (
	"context"

	"github.com/containerd/containerd/v2/cmd/containerd-shim-runc-v2/manager"
	"github.com/containerd/containerd/v2/pkg/shim"

	_ "containix.dev/pkg/shimservice"
)

func main() {
	shim.Run(
		context.Background(),
		manager.NewShimManager("io.containerd.containix.v1"),
	)
}
