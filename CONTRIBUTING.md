# Contributing to containix

Thanks for your interest. This is an early-stage project — APIs and internals will change. Open an issue before sinking significant time into a PR so we can align.

## Dev environment

[Nix](https://nixos.org/download) is required. [direnv](https://direnv.net/) is recommended but optional.

```bash
git clone https://github.com/atmask/containix
cd containix
direnv allow          # first time only — loads the Nix dev shell
```

Without direnv:

```bash
nix develop
```

The shell installs Go, [just](https://github.com/casey/just), [gopls](https://pkg.go.dev/golang.org/x/tools/gopls), [golangci-lint](https://golangci-lint.run/), and [pre-commit](https://pre-commit.com/) with the configured hooks (gofmt, govet, golangci-lint, nixpkgs-fmt, typos, generic hygiene). The hooks auto-install into `.git/hooks/` on first shell entry.

## Common commands

| Command | Effect |
|---|---|
| `nix develop -c go test ./...` | Run the Go test suite (fast tests + integration if `nix` is available) |
| `nix develop -c go test -short ./...` | Fast tests only — skips Nix-integration tests |
| `nix build .#default` | Build the shim binary via Nix |
| `just build` | Cross-compile shim for linux/arm64 |
| `just install` | Build + install + restart containerd (Linux only) |
| `just test` | Smoke test against a Linux VM (alpine + `nixpkgs#hello`) |
| `pre-commit run --all-files` | Run all hooks against all files |

## Testing changes against a real shim

The Go test suite covers unit logic. To validate end-to-end behavior (CRI integration, container lifecycle, real Nix builds), you need a Linux host with containerd installed. See the [tutorials in `docs/tutorials/`](./docs/tutorials/) for setting up a local dev environment — a NixOS UTM VM on macOS or k3s on a small Linux machine both work.

The fastest dev loop:

1. Edit code.
2. `just build` — cross-compile.
3. `scp bin/containerd-shim-containix-v1 user@vm:/tmp/`
4. On the VM: `sudo install -m 755 /tmp/containerd-shim-containix-v1 /var/lib/containix/bin/ && sudo systemctl restart containerd`
5. `just test` (against your VM).

If you're using the NixOS module from this flake, `nixos-rebuild switch --flake .#your-host --target-host root@vm` is the equivalent.

## Commit conventions

No strict convention required. Descriptive imperative-mood subjects (`fix Create handler RegisterTTRPC discovery`) are appreciated. Conventional Commits prefixes (`feat:`, `fix:`, `chore:`) are accepted but not required.

Pre-commit hooks run automatically on `git commit`. If you bypass them (`--no-verify`), CI will catch the same issues.

## CI

Every push and pull request runs four jobs on GitHub Actions:

- `nix flake check` + `nix build .#default`
- `go test -race ./...`
- `pre-commit run --all-files`
- Cross-builds for `linux/amd64` and `linux/arm64`

Green CI is required for merge. The badge at the top of the README links to the latest run.

## Project layout

```
cmd/containerd-shim-containix-v1/   shim entry point (main.go)
pkg/shimservice/                    Task service implementation + plugin registration
pkg/nix/                            Annotations, build, closure, rootfs assembly
docs/                               Architecture, tutorials (build journal)
site/                               Project landing page (containix.dev)
flake.nix                           Package + NixOS module + dev shell
.pre-commit-config.yaml             Hook configuration
.github/workflows/                  CI
```

## Security

See [SECURITY.md](./SECURITY.md) for reporting vulnerabilities.

## Code of conduct

Be respectful and constructive. Disagree about ideas, not people. Don't be a jerk.
