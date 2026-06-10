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
              go version
            '';
          };
        });
}
