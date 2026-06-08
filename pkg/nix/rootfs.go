package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// PrepareRootfs modified the OCI bundle to mount hte Nix store paths
// and set the entrypoint
func PrepareRootfs(bundlePath string, storePath string, cfg *ContainixConfig, closurePaths []string) error {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("reading spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parsing spec: %w", err)
	}

	rootfs := spec.Root.Path
	if !filepath.IsAbs(rootfs) {
		rootfs = filepath.Join(bundlePath, rootfs)
	}

	// Create the mount targets and add bind mounts for each
	// store path of the closure
	for _, sp := range closurePaths {
		base := filepath.Base(sp)
		// Add bind mount to the OCI spec
		spec.Mounts = append(spec.Mounts, ocispec.Mount{
			Destination: filepath.Join("/nix/store", base),
			Source:      sp,
			Type:        "bind",
			Options:     []string{"rbind", "ro"},
		})

	}

	// Override entrypoint if specified
	if cfg.Entrypoint != "" {
		entrypoint := cfg.Entrypoint
		if !strings.HasPrefix(entrypoint, "/") {
			entrypoint = filepath.Join(storePath, entrypoint)
		}
		spec.Process.Args = []string{cfg.Entrypoint}
	}

	modified, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshaling spec: %w", err)
	}
	if err := os.WriteFile(specPath, modified, 0644); err != nil {
		return fmt.Errorf("writing spec: %w", err)
	}

	return nil
}
