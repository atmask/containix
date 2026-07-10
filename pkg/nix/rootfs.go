package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// PrepareRootfs modifies the OCI bundle to mount the Nix store paths
// and set the entrypoint.
func PrepareRootfs(bundlePath string, flakeOutput FlakeOutput, cfg *ContainixConfig, closurePaths []string) error {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return fmt.Errorf("reading spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return fmt.Errorf("parsing spec: %w", err)
	}

	// Add bind mounts for each store path of the closure. runc creates the
	// mount target directories inside the container's mount namespace at
	// start time, so we don't need to pre-create them on the host.
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
	entrypoint, err := resolveEntrypoint(cfg, flakeOutput)
	if err != nil {
		return fmt.Errorf("resolving entrypoint: %w", err)
	}
	spec.Process.Args = []string{entrypoint}

	modified, err := json.Marshal(spec)
	if err != nil {
		return fmt.Errorf("marshaling spec: %w", err)
	}
	if err := os.WriteFile(specPath, modified, 0644); err != nil {
		return fmt.Errorf("writing spec: %w", err)
	}

	return nil
}
