{
  description = "containix — a containerd shim that runs Nix flakes on Kubernetes";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      perSystem = flake-utils.lib.eachSystem [
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
              src = ./.;
              vendorHash = "sha256-WFoF47S/FmejZGsOVkVuZCsPya0szX2m0nqxRtRsQqU=";
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

            # Empty stub image so containix consumers can pass a
            # CRI-compliant OCI image reference in their pod specs.
            mkStubImage = architecture: pkgs.dockerTools.buildImage {
              name = "ghcr.io/atmask/containix-empty";
              tag = "0.1.0";
              inherit architecture;
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
            packages.default = containixPkg;
            packages.stub-image-amd64 = mkStubImage "amd64";
            packages.stub-image-arm64 = mkStubImage "arm64";
          });
    in
    perSystem // {
      nixosModules.default = { config, lib, pkgs, ... }:
        let
          cfg = config.services.containix;
        in
        {
          options.services.containix = {
            enable = lib.mkEnableOption "the containix containerd runtime shim";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              defaultText = lib.literalExpression
                "containix.packages.\${pkgs.system}.default";
              description = ''
                The containix shim package to install on this node. Defaults to
                the flake's own package output for the host's system.
              '';
            };

            runtimeName = lib.mkOption {
              type = lib.types.str;
              default = "containix";
              description = ''
                Name used in containerd's runtime registration block and in the
                Kubernetes RuntimeClass that routes pods to this shim.
              '';
            };

            runtimeType = lib.mkOption {
              type = lib.types.str;
              default = "io.containerd.containix.v1";
              description = ''
                containerd runtime_type string. Must end in `.v<N>` matching
                the shim binary's filename suffix (containerd-shim-containix-v1).
              '';
            };
          };

          config = lib.mkIf cfg.enable {
            # Containerd must be running for the runtime registration to take
            # effect. mkDefault lets a consumer override (e.g., if they want
            # to manage containerd separately).
            virtualisation.containerd.enable = lib.mkDefault true;

            # Register the runtime in containerd's CRI config. The TOML output
            # looks like:
            #   [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
            #     runtime_type = "io.containerd.containix.v1"
            #     pod_annotations = ["containix.dev/*"]
            #     container_annotations = ["containix.dev/*"]
            virtualisation.containerd.settings.plugins."io.containerd.grpc.v1.cri".containerd.runtimes.${cfg.runtimeName} = {
              runtime_type = cfg.runtimeType;

              # CRI drops custom annotations before they reach the OCI runtime
              # spec unless each glob is opted in here. Without this, the pod's
              # containix.dev/flake annotation never reaches the shim (FromBundle
              # sees nothing → passthrough to runc → the flake silently never
              # runs). Pod-level covers annotations set on Pod metadata (the
              # documented form); container-level covers per-container use.
              pod_annotations = [ "containix.dev/*" ];
              container_annotations = [ "containix.dev/*" ];
            };

            # Put the shim binary on containerd's PATH so it can resolve
            # runtime_type → containerd-shim-containix-v1 binary at exec time.
            systemd.services.containerd.path = [
              cfg.package
              pkgs.nix
              pkgs.runc
            ];

            # Also expose system-wide for ad-hoc operator commands like
            # `containerd-shim-containix-v1 -info` when debugging.
            environment.systemPackages = [ cfg.package ];
          };
        };
    };
}
