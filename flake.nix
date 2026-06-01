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
    ] (system:
      let
        pkgs = nixpkgs.legacyPackages.${system};
      in {
        devShells.default = pkgs.mkShell {
          name = "containix-dev";

          packages = with pkgs; [
            go              # toolchain (needs >= 1.26.3 for containerd v2.3.x)
            just            # task runner used by ./justfile
            git
            gopls           # Go language server (editor LSP)
            gotools         # goimports, etc.
            gofumpt         # stricter gofmt
            go-tools        # staticcheck and friends
            golangci-lint
          ];

          shellHook = ''
            echo "containix dev shell"
            go version
          '';
        };
      });
}
