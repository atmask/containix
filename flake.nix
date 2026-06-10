{
  description = "containix — a containerd shim that runs Nix flakes on Kubernetes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    flake-utils.lib.eachSystem [
      "aarch64-darwin"
      "x86_64-darwin"
      "aarch64-linux"
      "x86_64-linux"
    ]
      (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          isLinux = pkgs.stdenv.isLinux;

          containixPkg = pkgs.buildGoModule {
            pname = "containerd-shim-containix-v1";
            version = "0.1.0";
            src = "./.";
            vendorHash = pkgs.lib.fakeHash;
            subPackages = [ "cmd/containerd-shim-containix-v1" ];
            env.CGO_ENABLED = "0";
            meta = with pkgs.lib; {
              description = "containerd shim that runs Nix flakes";
              homepage = "https://github.com/atmask/containix";
              license = licenses.asl20;
              platforms = platforms.linux;
              mainProgram = "containerd-shim-containix-v1";
            };
          };
        in
        {
          devShells.default = pkgs.mkShell {
            name = "containix-dev";

            packages = with pkgs; [
              # Toolchain
              go # needs >= 1.26.3 for containerd v2.3.x
              just # task runner used by ./justfile
              git

              # Go dev tooling
              gopls # Go language server (editor LSP)
              gotools # goimports, etc.
              gofumpt # stricter gofmt
              go-tools # staticcheck and friends
              golangci-lint

              # Pre-commit hook framework + the tools the configured hooks invoke
              pre-commit
              nixpkgs-fmt
              shellcheck
              typos
            ];

            shellHook = ''
              # Install the pre-commit hook into .git/hooks/ if it isn't already.
              # Idempotent: pre-commit detects when it's already installed and skips.
              if [ -d .git ]; then
                ${pkgs.pre-commit}/bin/pre-commit install --install-hooks > /dev/null 2>&1 || true
              fi

              echo "containix dev shell"
              echo
              echo "Tools:"
              echo "  $(go version)"
              echo "  just $(just --version 2>/dev/null | awk '{print $2}')"
              echo
              echo "Common commands:"
              echo "  just build    — cross-compile shim for linux/arm64 (works on Mac too)"
              if [ "$(uname)" = "Linux" ]; then
                echo "  just install  — build + install + restart containerd"
                echo "  just test     — install + smoke test (alpine + nixpkgs#hello)"
                echo "  just logs     — tail containerd logs filtered to containix"
              else
                echo "  just install  — Linux only (run from a Linux dev VM)"
                echo "  just test     — Linux only (run from a Linux dev VM)"
                echo "  just logs     — Linux only (run from a Linux dev VM)"
              fi
              echo
            '';
          };
        } // pkgs.lib.optionalAttrs isLinux {
          pkgs.default = containixPkg;
        });
}
