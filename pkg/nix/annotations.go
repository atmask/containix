package nix

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	ocispec "github.com/opencontainers/runtime-spec/specs-go"
)

const (
	AnnotationFlake         = "containix.dev/flake"
	AnnotationEntrypoint    = "containix.dev/entrypoint"
	AnnotationTimeout       = "containix.dev/timeout"            // e.g. "120s", "5m"
	AnnotationContainerType = "io.kubernetes.cri.container-type" // injected by CRI
)

// DefaultBuildTimeout is the upper bound on a single Nix build invocation
// when the pod doesn't override via the containix.dev/timeout annotation.
// Tuned for "fetch a moderately sized closure from a binary cache" — too
// short for first-time-on-node fresh builds, fine for cached pulls.
const DefaultBuildTimeout = 2 * time.Minute

// MaxBuildTimeout caps how long a single container can wait for Nix work,
// regardless of what the pod requests. Prevents a misbehaving annotation
// from holding a pod in ContainerCreating forever.
const MaxBuildTimeout = 30 * time.Minute

type ContainixConfig struct {
	FlakeRef   string
	Entrypoint string
	// Timeout is the maximum time allowed for the combined Nix build +
	// closure resolution for this container. Always populated; defaults to
	// DefaultBuildTimeout and is clamped to MaxBuildTimeout.
	Timeout time.Duration
}

// FromBundle reads the containix annotations from the OCI bundle's config.json.
// Returns nil if no containix annotations are present (passthrough to runc).
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

	return &ContainixConfig{
		FlakeRef:   flakeRef,
		Entrypoint: spec.Annotations[AnnotationEntrypoint],
		Timeout:    parseTimeout(spec.Annotations[AnnotationTimeout]),
	}, nil
}

// parseTimeout decodes the containix.dev/timeout annotation.
//
//   - Empty/missing  → DefaultBuildTimeout
//   - Invalid format → DefaultBuildTimeout (log-then-ignore; surfacing an
//     error would block the container for a malformed string)
//   - Zero / negative → DefaultBuildTimeout (a 0s timeout would always fail)
//   - Above MaxBuildTimeout → clamped to MaxBuildTimeout
//
// Format follows Go's time.ParseDuration: "30s", "2m", "1h30m".
func parseTimeout(raw string) time.Duration {
	if raw == "" {
		return DefaultBuildTimeout
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return DefaultBuildTimeout
	}
	if d > MaxBuildTimeout {
		return MaxBuildTimeout
	}
	return d
}
