package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

// PrepareRootfs modified the OCI bundle to mount hte Nix store paths
// and set the entrypoint
func PrepareRootfs(bundlePath string, cfg *ContainixConfig, storePaths []string) error {
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

	// nixStore := filepath.Join(rootfs, "nix", "store")
	// if err := os.MkdirAll(nixStore, 0755); err != nil {
	// 	return fmt.Errorf("creating /nix/store in rootfs: %w", err)
	// }

	// Create the mount targets and add bind mounts for each
	// store path of the closure
	for _, sp := range storePaths {
		base := filepath.Base(sp)
		// mountpoint := filepath.Join(nixStore, base)
		//
		// // Create the mount target dir
		// if err := os.MkdirAll(mountpoint, 0755); err != nil {
		// 	return fmt.Errorf("creating mount target %s: %w", mountpoint, err)
		// }

		// Add bind mount to the OCI spec
		spec.Mounts = append(spec.Mounts, ocispec.Mount{
			Destination: filepath.Join("/nix/store", base),
			Source:      sp,
			Type:        "bind",
			Options:     []string{"rbind", "ro"},
		})

	}
	//
	// // Create essential dirs that procs expect
	// for _, dir := range []string{"tmp", "proc", "sys", "dev", "etc"} {
	// 	if err := os.MkdirAll(filepath.Join(rootfs, dir), 0755); err != nil {
	// 		return fmt.Errorf("creating /%s: %w", dir, err)
	// 	}
	// }

	// Override entrypoint if specified
	if cfg.Entrypoint != "" {
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
