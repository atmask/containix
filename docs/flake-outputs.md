# Adding `packages.default` and `nixosModules.default` to the flake

A step-by-step guide for the implementer. Assumes you're new to Nix flake outputs but comfortable with the existing `devShells.default` in `flake.nix`.

By the end you'll have:

- `nix build .#default` producing the shim binary in `result/bin/`.
- A NixOS module that, when imported, registers the containix runtime with containerd and installs the binary.
- Both consumable from other flakes (used by `infra-provisioning` and by anyone deploying containix).

---

## What you're adding and why

Right now `flake.nix` exposes only one output: `devShells.default` (the dev environment). You're adding two more:

1. **`packages.default`** — a Nix derivation that builds the shim binary. After this, `nix build .#default` produces a usable binary. CI can build it. Consumers can install it via `nix profile install github:atmask/containix` or reference it from their own flakes.

2. **`nixosModules.default`** — a NixOS module (a function, not a derivation) that takes the package and wires it into a NixOS system: enables containerd, registers the containix runtime, puts the shim binary on PATH. Consumers do `imports = [ inputs.containix.nixosModules.default ]; services.containix.enable = true;` and everything happens automatically.

Why two outputs and not one? A package and a module serve different roles:

| Output type | What it is | Consumer command |
|---|---|---|
| `packages.default` | A buildable thing (the binary in `/nix/store/`) | `nix build .#default` |
| `nixosModules.default` | A configuration recipe (options + side effects) | `imports = [ ... ]` in NixOS config |

The module **uses** the package internally, but they're separate outputs because consumers may want one without the other (e.g., someone wanting to run the shim outside Kubernetes would use just the package).

---

## Background: what is `buildGoModule`?

`buildGoModule` is a Nix function in nixpkgs that knows how to build Go projects. You give it a spec like:

```nix
pkgs.buildGoModule {
  pname = "myapp";
  version = "1.0.0";
  src = ./.;
  vendorHash = "sha256-...";
  ...
}
```

and it produces a derivation that, when built, runs `go build` inside a sandbox and produces the binary at `$out/bin/myapp`. Key responsibilities:

- Sets up the Go toolchain at the version specified by your `go.mod`.
- Reads `go.mod` and `go.sum`, downloads all dependencies.
- Verifies the dependencies match `vendorHash` (Nix's way of pinning what got downloaded — the "vendor hash dance" below).
- Runs `go build` for each `subPackage`.
- Installs the resulting binaries into `$out/bin/`.

You don't write Go-specific Nix from scratch; `buildGoModule` handles it.

---

## Background: what is a NixOS module?

A NixOS module is a Nix function with a specific shape:

```nix
{ config, lib, pkgs, ... }:
{
  options = { ... };   # declares what consumers can configure
  config  = { ... };   # what to actually do
}
```

- **`options`** declares typed configuration knobs that consumers can set. For example, `services.nginx.enable = true;` is consumers setting an option declared by the nginx module.
- **`config`** is what happens when those options are set. Side effects: install packages, write files, enable systemd units, etc.

The module function receives `{ config, lib, pkgs, ... }`:
- `config` is the FULLY EVALUATED system configuration (after all modules merge).
- `lib` is nixpkgs' standard library (helpers like `mkIf`, `mkOption`, `mkEnableOption`).
- `pkgs` is the package set for the system being built.

The pattern is:
1. Declare options.
2. Use `config = lib.mkIf cfg.enable { ... };` to make side effects conditional on the consumer enabling the module.

---

## Step 1 — Add `packages.default` to `flake.nix`

You're going to define a `buildGoModule` derivation and expose it as `packages.default`. The wrinkle: it only builds on Linux (the shim source has `//go:build linux`), so it should only be exposed on Linux systems.

### 1.1 Open `flake.nix`

The current structure:

```nix
outputs = { self, nixpkgs, flake-utils }:
  flake-utils.lib.eachSystem [...] (system:
    let pkgs = ...; in
    {
      devShells.default = pkgs.mkShell { ... };
    });
```

You want to add a `packages.default` next to `devShells.default`, but only for Linux systems.

### 1.2 Define the package inside the `let` block

Inside the `let pkgs = nixpkgs.legacyPackages.${system};` block, add:

```nix
isLinux = pkgs.stdenv.hostPlatform.isLinux;

containixPkg = pkgs.buildGoModule {
  pname = "containerd-shim-containix-v1";
  version = "0.1.0";
  src = ./.;
  # We'll fix this in step 2.
  vendorHash = pkgs.lib.fakeHash;
  subPackages = [ "cmd/containerd-shim-containix-v1" ];
  env.CGO_ENABLED = "0";
  meta = with pkgs.lib; {
    description = "containerd shim that runs Nix flakes on Kubernetes";
    homepage    = "https://github.com/atmask/containix";
    license     = licenses.asl20;
    platforms   = platforms.linux;
    mainProgram = "containerd-shim-containix-v1";
  };
};
```

What each field does:

- `pname` + `version` → the store path will be `/nix/store/<hash>-containerd-shim-containix-v1-0.1.0/`.
- `src = ./.;` — current directory as the source. Nix copies it into the build sandbox.
- `vendorHash` — placeholder for now; fix in Step 2.
- `subPackages` — only build the shim's `main` package, not the library packages (which have no main).
- `env.CGO_ENABLED = "0";` — same flag your justfile uses; produces a statically linked binary.
- `meta.platforms = platforms.linux;` — declares Linux-only. Used by nixpkgs tools and prevents accidental Darwin builds.

### 1.3 Expose it as an output, Linux-only

After the `in` keyword, you currently return:

```nix
{
  devShells.default = pkgs.mkShell { ... };
}
```

Change it to:

```nix
{
  devShells.default = pkgs.mkShell { ... };
} // pkgs.lib.optionalAttrs isLinux {
  packages.default = containixPkg;
}
```

`//` is Nix's set-merge operator. `optionalAttrs isLinux { ... }` evaluates to an empty set on Darwin and to `{ packages.default = ...; }` on Linux. Net effect: `packages.default` only appears as a flake output on Linux systems.

### 1.4 Save and validate the syntax

```bash
nix flake check --no-build 2>&1 | tail -10
```

You should see the existing dev shell check pass, plus the new `packages.aarch64-linux.default` (or `x86_64-linux.default`) derivation evaluating. If you're on Darwin, you won't see `packages.aarch64-darwin.default` — that's correct, we deliberately only expose it on Linux.

---

## Step 2 — The vendor hash dance

You set `vendorHash = pkgs.lib.fakeHash;` as a placeholder. This will fail at build time with a clear error showing the real hash. You then paste the real hash back into `flake.nix`.

### 2.1 Trigger a build

On a Linux machine (or your NixOS UTM VM):

```bash
nix build .#default 2>&1 | tail -10
```

On macOS, you can't natively build a Linux derivation, but you can try with the linux system explicitly:

```bash
nix build .#packages.aarch64-linux.default --system aarch64-linux 2>&1 | tail -10
```

(This requires either `binfmt_misc` set up for aarch64-linux emulation, a remote Linux builder configured in `~/.config/nix/nix.conf`, or a binary cache hit. If none apply, do this from your NixOS VM instead.)

### 2.2 Read the error

You'll see something like:

```
error: hash mismatch in fixed-output derivation '/nix/store/.../...-vendor.drv':
         specified: sha256-AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA=
            got:    sha256-h4sH+5xY3uMABLOOhd2cKLO6S0YkA8Edk9oCl5jVnPo=
```

The `got:` value is the real vendor hash.

### 2.3 Paste it into `flake.nix`

Replace `pkgs.lib.fakeHash` with the literal hash string from the error:

```nix
vendorHash = "sha256-h4sH+5xY3uMABLOOhd2cKLO6S0YkA8Edk9oCl5jVnPo=";
```

(Your actual hash will be different. Use whatever the error printed.)

### 2.4 Rebuild

```bash
nix build .#default
```

Should now succeed and produce a `result/` symlink pointing at the built binary.

### 2.5 Verify

```bash
ls -la result/bin/
# Expected: containerd-shim-containix-v1

file result/bin/containerd-shim-containix-v1
# Expected: ELF 64-bit LSB executable, ARM aarch64, ... statically linked
```

If you see the right binary, the package output works.

### Vendor hash maintenance

The vendor hash is computed from `go.sum`. Any time you change Go dependencies (and `go.sum` updates), the hash will change. The build will fail with the same "expected X, got Y" error, and you paste the new hash in. CI catches drift automatically — when `go.sum` updates without a matching `vendorHash` update, the build fails loudly.

---

## Step 3 — Add `nixosModules.default`

A NixOS module is structurally different from a package: it's a function, evaluated lazily by the consumer's NixOS system. Modules live OUTSIDE `flake-utils.lib.eachSystem` because they aren't tied to a specific system; the consuming NixOS evaluates them for whatever system its host is.

### 3.1 Restructure the outputs scaffolding

Change the outer shape from:

```nix
outputs = { self, nixpkgs, flake-utils }:
  flake-utils.lib.eachSystem [...] (system: { ... });
```

to:

```nix
outputs = { self, nixpkgs, flake-utils }:
  let
    perSystem = flake-utils.lib.eachSystem [...] (system: { ... });
  in
  perSystem // {
    nixosModules.default = { ... };   # filled in below
  };
```

This `//` merge pattern adds `nixosModules.default` alongside the per-system outputs.

### 3.2 Write the module

Inside `nixosModules.default`, write the standard NixOS module function:

```nix
nixosModules.default = { config, lib, pkgs, ... }:
  let
    cfg = config.services.containix;
  in
  {
    options.services.containix = {
      enable = lib.mkEnableOption "the containix containerd shim runtime";

      package = lib.mkOption {
        type = lib.types.package;
        default = self.packages.${pkgs.system}.default;
        defaultText = lib.literalExpression
          "containix.packages.\${pkgs.system}.default";
        description = "The containix shim package to install on this node.";
      };

      runtimeName = lib.mkOption {
        type = lib.types.str;
        default = "containix";
        description = ''
          Name used in containerd's runtime registration and in the
          Kubernetes RuntimeClass that routes pods to this shim.
        '';
      };

      runtimeType = lib.mkOption {
        type = lib.types.str;
        default = "io.containerd.containix.v1";
        description = ''
          containerd runtime_type string. Must end in `.v<N>` matching the
          shim binary's filename suffix (containerd-shim-containix-v1).
        '';
      };
    };

    config = lib.mkIf cfg.enable {
      # Containerd must be running for the runtime registration to take effect.
      virtualisation.containerd.enable = lib.mkDefault true;

      # Register the runtime in containerd's CRI config.
      virtualisation.containerd.settings
        .plugins."io.containerd.grpc.v1.cri".containerd.runtimes.${cfg.runtimeName} = {
          runtime_type = cfg.runtimeType;
        };

      # Put the shim binary on containerd's PATH so it can resolve
      # `runtime_type` → `containerd-shim-containix-v1` binary.
      systemd.services.containerd.path = [ cfg.package ];

      # Expose system-wide for ad-hoc operator commands like
      # `containerd-shim-containix-v1 -info`.
      environment.systemPackages = [ cfg.package ];
    };
  };
```

What each piece does:

- **`options.services.containix.enable`** — the on/off switch. `mkEnableOption` is a helper that creates a boolean option with default `false` and a standard description.
- **`options.services.containix.package`** — defaults to `self.packages.${pkgs.system}.default`. The `self` is from the outer `outputs = { self, ... }:` and `pkgs.system` is determined by the consuming NixOS system (always Linux). `defaultText` is what shows up in generated documentation; without it, the docs would inline the full derivation.
- **`options.services.containix.runtimeName` / `runtimeType`** — make these configurable in case someone wants to deploy multiple containix variants side by side.
- **`config = lib.mkIf cfg.enable { ... }`** — everything in here only takes effect when `services.containix.enable = true`. `mkIf` evaluates lazily, so the module is harmless when disabled.
- **`virtualisation.containerd.enable = lib.mkDefault true;`** — `mkDefault` means "enable containerd by default, but let the consumer override." If the consumer is already enabling containerd elsewhere, no conflict.

### 3.3 Save and validate

```bash
nix flake check 2>&1 | tail -20
```

You should see no errors. The flake now has three outputs: `devShells.default`, `packages.default` (Linux-only), and `nixosModules.default`.

---

## Step 4 — Test the module

You need to import it into a NixOS host and verify the runtime is registered.

### 4.1 In your nixos-config flake (separate repo)

Add containix as a flake input:

```nix
# In ~/Github/atmask/nixos-config/flake.nix
inputs.containix.url = "github:atmask/containix";    # or path:/Users/benmask/Github/atmask/containix during local dev
```

In a host config (e.g., `modules/containix-dev.nix`), import the module:

```nix
{ pkgs, inputs, ... }: {
  imports = [ inputs.containix.nixosModules.default ];

  services.containix.enable = true;
}
```

(Make sure `specialArgs` passes `inputs` through to the modules; check your existing flake's `nixosSystem` invocation.)

### 4.2 Apply the config

```bash
sudo nixos-rebuild switch --flake .#nixos-utm   # or whatever your host name is
```

### 4.3 Verify

```bash
# The shim binary should be on PATH
which containerd-shim-containix-v1

# It should respond to -info
sudo containerd-shim-containix-v1 -info

# Containerd's config should show the runtime registered
sudo containerd config dump | grep -A 3 containix
```

Expected: a `[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]` block with `runtime_type = "io.containerd.containix.v1"`.

You should now be able to `sudo ctr run --runtime io.containerd.containix.v1 ...` exactly as you do today, but the shim came from the NixOS module rather than being manually `install`-ed.

---

## Troubleshooting

### `error: hash mismatch in fixed-output derivation`

You haven't done the vendor-hash dance (Step 2.2-2.3). Copy the `got:` value into `flake.nix`'s `vendorHash` field.

### `error: builder for '...' failed with exit code 1` referencing `//go:build linux`

You're trying to build the package on a non-Linux system. The package is correctly Linux-only. Either:
- Run the build on Linux (your UTM VM).
- Use `--system aarch64-linux` to ask Nix to find a Linux builder.

### `error: attribute 'default' missing` when running `nix build .#default` on macOS

Same issue — `packages.default` doesn't exist for Darwin systems. We deliberately scoped it Linux-only.

### `error: services.containix.enable: value is false while a true is expected` during `nixos-rebuild`

You forgot to set `services.containix.enable = true;` in the consuming host's config. The module is imported but not activated.

### Module changes don't take effect after `nixos-rebuild switch`

The flake inputs in your nixos-config are pinned. To pick up containix changes:

```bash
cd ~/Github/atmask/nixos-config
nix flake lock --update-input containix
sudo nixos-rebuild switch --flake .
```

---

## What you've learned

By completing this:

- **`buildGoModule` for Go packaging** — the vendor hash workflow, `subPackages`, CGO control.
- **The `eachSystem` / set-merge pattern** — how to mix system-specific outputs (packages, dev shells) with system-independent ones (NixOS modules).
- **NixOS module structure** — `options` vs `config`, `mkIf`, `mkDefault`, `mkEnableOption`, `mkOption`.
- **Cross-flake references** — how `self` and `pkgs.system` resolve to the right package when one flake's module is consumed by another flake's NixOS system.

These are the building blocks for any Nix flake that distributes Go software with a NixOS module. Reusable knowledge.

---

## What's next after this lands

Once `nix build .#default` works:

1. **CI workflow** can include `nix build .#default` as a check.
2. **Release workflow** can use `nix build` + uploading `result/bin/...` as the release artifact.
3. **README badges** can point at the working CI.
4. **`infra-provisioning`** can consume containix as a flake input and use `nixosModules.default` in its host configs (your private next step, not blocking the public release).
