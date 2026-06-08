package nix

import (
	"fmt"
	"os/exec"
	"strings"
)

// Build resolves a flake reference to a Nix store path
func Build(flakeRef string) (string, error) {
	cmd := exec.Command("nix", "build",
		"--no-link",
		"--print-out-paths",
		"--no-update-lock-file",
		"--no-write-lock-file",
		flakeRef,
	)

	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("nix build failed: %s", string(exitErr.Stderr))
		}
		return "", fmt.Errorf("nix build: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}

// Closure returns all store paths in the closure of the given path
func Closure(storePath string) ([]string, error) {
	cmd := exec.Command("nix-store", "-qR", storePath)
	out, err := cmd.Output()
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
