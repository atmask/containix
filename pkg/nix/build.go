package nix

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os/exec"
	"path/filepath"
	"strings"
)

// ErrTimeout is returned when a Nix subprocess is killed because its context
// deadline expired. Distinct from a normal command failure so callers can
// surface a clearer error to operators.
var ErrTimeout = errors.New("nix subprocess timed out")

type FlakeOutput struct {
	StorePath   string
	MainProgram string //empty if not declared by the derivation
}

// Build resolves a flake reference to a Nix store path.
//
// Honors the context's deadline (if any) by killing the underlying nix
// subprocess when ctx is canceled. Caller is responsible for deciding the
// timeout; typically derived from the container's containix.dev/timeout
// annotation.
func Build(ctx context.Context, flakeRef string) (FlakeOutput, error) {
	out, err := exec.CommandContext(ctx, "nix", "build",
		"--no-link",
		"--print-out-paths",
		"--no-update-lock-file",
		"--no-write-lock-file",
		flakeRef,
	).Output()

	// Check context first — on a deadline kill, err is just "signal: killed",
	// which isn't actionable. Translate to ErrTimeout so callers can surface a
	// clear message (mirrors Closure below).
	if ctx.Err() == context.DeadlineExceeded {
		return FlakeOutput{}, fmt.Errorf("%w: nix build %s", ErrTimeout, flakeRef)
	}
	if err != nil {
		return FlakeOutput{}, fmt.Errorf("nix build: %w", err)
	}

	storePath := strings.TrimSpace(string(out))

	metaCmd := exec.CommandContext(ctx, "nix", "eval", "--raw",
		flakeRef, "--apply", `p: p.meta.mainProgram or ""`,
	)

	metaOut, err := metaCmd.Output()
	// A timeout here means the whole creation budget is exhausted — fail loudly
	// like the build/closure steps rather than silently returning no mainProgram.
	if ctx.Err() == context.DeadlineExceeded {
		return FlakeOutput{}, fmt.Errorf("%w: nix eval meta.mainProgram %s", ErrTimeout, flakeRef)
	}
	if err != nil {
		// Not a timeout — a genuine eval failure. The build already succeeded,
		// so mainProgram is best-effort: log and continue. resolveEntrypoint will
		// emit a clear error if no entrypoint can be determined.
		log.Printf("[containix] meta.mainProgram eval failed for %s: %v", flakeRef, err)
	}
	mainProgram := strings.TrimSpace(string(metaOut))
	return FlakeOutput{StorePath: storePath, MainProgram: mainProgram}, nil
}

func resolveEntrypoint(cfg *ContainixConfig, flake FlakeOutput) (string, error) {
	switch {
	case strings.HasPrefix(cfg.Entrypoint, "/"):
		// Explicit absolute path — use as-is.
		return cfg.Entrypoint, nil

	case cfg.Entrypoint != "":
		// Explicit relative path — join against the flake's store path.
		return filepath.Join(flake.StorePath, cfg.Entrypoint), nil

	case flake.MainProgram != "":
		// Default: bin/<mainProgram> in the flake's output.
		return filepath.Join(flake.StorePath, "bin", flake.MainProgram), nil

	default:
		return "", fmt.Errorf(
			"no entrypoint resolved for flake %q:\n"+
				"  - no %q annotation provided\n"+
				"  - flake does not declare meta.mainProgram on its default package\n"+
				"fix: either add the annotation or set meta.mainProgram in the derivation",
			cfg.FlakeRef, AnnotationEntrypoint)
	}
}

// Closure returns all store paths in the closure of the given path.
//
// Honors the context's deadline (if any). Closure queries should be fast
// (no network, no compilation) but can still hang on misbehaving daemons.
func Closure(ctx context.Context, storePath string) ([]string, error) {
	cmd := exec.CommandContext(ctx, "nix-store", "-qR", storePath)
	out, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("%w: nix-store -qR %s", ErrTimeout, storePath)
	}
	if err != nil {
		return nil, fmt.Errorf("nix-store -qR: %w", err)
	}

	var paths []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			paths = append(paths, line)
		}
	}
	return paths, nil
}
