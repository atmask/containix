package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	AnnotationFlake         = "containix.dev/flake"
	AnnotationEntrypoint    = "containinx/dev/entrypoint"
	AnnotationContainerType = "io.kubernetes.cri.container-type" // injected but CRI
)

type ContainixConfig struct {
	FlakeRef   string
	Entrypoint string
}

// FromBundle reads the containix annotations from the OCI bundle's config.json
// Returns nil if no containix annotations are present (passthrough to runc)
func FromBundle(bundlePath string) (*ContainixConfig, error) {
	specPath := filepath.Join(bundlePath, "config.json")
	data, err := os.ReadFile(specPath)
	if err != nil {
		return nil, fmt.Errorf("reading OCI spec: %w", err)
	}

	var spec ocispec.Spec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("parsing OCI spec: %w", err)
	}

	// skip sandbox containers - they need the upstream pause image, not Nix
	if spec.Annotations[AnnotationContainerType] == "sandbox" {
		return nil, nil
	}

	flakeRef, ok := spec.Annotations[AnnotationFlake]
	if !ok {
		return nil, nil // not a containix container
	}

	entrypoint := spec.Annotations[AnnotationEntrypoint]

	return &ContainixConfig{
		FlakeRef:   flakeRef,
		Entrypoint: entrypoint,
	}, nil

}
