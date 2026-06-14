package nix

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// ErrTimeout is returned when a Nix subprocess is killed because its context
// deadline expired. Distinct from a normal command failure so callers can
// surface a clearer error to operators.
var ErrTimeout = errors.New("nix subprocess timed out")

// Build resolves a flake reference to a Nix store path.
//
// Honors the context's deadline (if any) by killing the underlying nix
// subprocess when ctx is canceled. Caller is responsible for deciding the
// timeout; typically derived from the container's containix.dev/timeout
// annotation.
func Build(ctx context.Context, flakeRef string) (string, error) {
	cmd := exec.CommandContext(ctx, "nix", "build",
		"--no-link",
		"--print-out-paths",
		"--no-update-lock-file",
		"--no-write-lock-file",
		flakeRef,
	)

	out, err := cmd.Output()
	// Check context first — the exec error is usually "signal: killed" which
	// isn't actionable. Translate to a timeout error if that's why we killed.
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%w: nix build %s", ErrTimeout, flakeRef)
	}
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", fmt.Errorf("nix build failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("nix build: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
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
