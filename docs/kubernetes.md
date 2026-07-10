# Running containix on Kubernetes

An operator-focused guide to getting containix working on a real cluster. If you
want the step-by-step teaching version, see
[tutorials/04-kubernetes.md](./tutorials/04-kubernetes.md); this page is the
condensed "get it running" path.

> **Status (v0.1.0):** experimental. The NixOS path below is the one that's been
> validated end-to-end. Everything here is `kubectl apply` + node prep — there is
> **no Helm chart, operator, or node-installer DaemonSet yet** (see
> [Roadmap](#roadmap)).

## The mental model (read this first)

The hard part of running containix isn't Kubernetes — it's **node preparation**.
A pod opts in with a `RuntimeClass`; everything else is machinery on the node:

```
Pod (runtimeClassName: containix)
  └─ RuntimeClass  handler: containix
       └─ containerd  [runtimes.containix]  runtime_type = io.containerd.containix.v1
            └─ containerd-shim-containix-v1   (binary on containerd's PATH)
                 └─ nix build <flake> → mount closure → runc
```

Two things have to line up on **every node that runs containix pods**:

1. The **shim binary** is on containerd's `PATH`, and containerd has the runtime
   registered (`runtimes.containix`).
2. A **Nix daemon** is available so the shim can resolve and build flakes.

If those aren't in place, pods sit in `ContainerCreating` — a green `kubectl
apply` of the RuntimeClass alone does nothing. Prep the nodes first.

## Per-node prerequisites

- **containerd** as the node's runtime (default on EKS/GKE and most managed k8s;
  bundled in k3s).
- **Nix daemon** running on the node.
- The **containix shim** on containerd's `PATH` + the runtime registered.
- A **binary cache** (cache.nixos.org, Cachix, Attic, …) for tolerable cold
  starts — see [Cold starts](#cold-starts--binary-cache).
- A **pullable stub image**. Its contents are ignored (the rootfs comes from
  Nix), but CRI requires an `image:` field.

## Step 1 — Prepare the nodes

Use a **dedicated, labeled node group** rather than every node. It keeps the
blast radius small and lets `nodeSelector` route only opted-in pods there.

### Option A — NixOS (recommended, validated)

Add containix as a flake input and enable the module on the node(s):

```nix
{
  inputs.containix.url = "github:atmask/containix";

  outputs = inputs @ { nixpkgs, containix, ... }: {
    nixosConfigurations.your-node = nixpkgs.lib.nixosSystem {
      modules = [
        ./your-node.nix
        containix.nixosModules.default
        { services.containix.enable = true; }
      ];
    };
  };
}
```

The module:

- enables `virtualisation.containerd`,
- registers the runtime as `runtimes.containix` with
  `runtime_type = "io.containerd.containix.v1"`,
- puts the shim, `nix`, and `runc` on containerd's `PATH`.

The registered name comes from `services.containix.runtimeName` (default
`containix`) — **this is the value your `RuntimeClass` `handler` must match.**
`runtimeType` (default `io.containerd.containix.v1`) is the containerd
`runtime_type`.

After `nixos-rebuild switch`, verify and smoke-test the node directly (no
Kubernetes needed):

```bash
sudo containerd config dump | grep -A2 runtimes.containix

sudo ctr image pull docker.io/library/busybox:latest
sudo ctr run --rm \
  --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="nixpkgs#hello" \
  docker.io/library/busybox:latest containix-check
# → prints: Hello, world!
```

(`nixpkgs#hello` declares `meta.mainProgram`, so no entrypoint annotation is
needed — the shim resolves it. See [Annotations](#annotations).)

### Option B — non-NixOS (manual)

Less turnkey, but works on any containerd node:

```bash
# 1. Install Nix (multi-user; provides the daemon)
curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh

# 2. Install the shim binary onto containerd's PATH
#    - build it:   nix build github:atmask/containix#default   (result/bin/…)
#    - or download the release asset for your arch from
#      https://github.com/atmask/containix/releases
sudo cp containerd-shim-containix-v1 /usr/local/bin/

# 3. Register the runtime in containerd
sudo tee -a /etc/containerd/config.toml <<'EOF'

[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix.options]
    SystemdCgroup = true   # set to match your cluster's cgroup driver
EOF

sudo systemctl restart containerd
```

**k3s** regenerates its containerd config on start, so edit the template instead
of `config.toml` directly (seed `config.toml.tmpl` from the rendered config, then
append the same `runtimes.containix` block, then `systemctl restart k3s`). Full
steps are in [tutorial 04](./tutorials/04-kubernetes.md#configure-containerd--k3s).

### Managed clusters (EKS, GKE, and friends)

You only control nodes through their image / user-data, so bake **Nix + the shim
binary + the containerd runtime block** into a custom node image (or a
`cloud-init`/bootstrap script) for a dedicated node group. This is the hardest
path today — there is no node-installer to do it for you. Pre-populating the Nix
store with common closures in the image also helps first-pod cold starts.

### Label the nodes

```bash
kubectl label node <node-name> containix.dev/runtime=true
```

## Step 2 — Create the RuntimeClass

```yaml
# deploy/runtimeclass.yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: containix
handler: containix            # MUST equal services.containix.runtimeName / runtimes.<name>
scheduling:
  nodeSelector:
    containix.dev/runtime: "true"   # only schedule onto prepared nodes
```

```bash
kubectl apply -f deploy/runtimeclass.yaml
```

The `scheduling.nodeSelector` ensures pods using this class only land on nodes you
actually prepared in Step 1.

## Step 3 — Deploy a workload

```yaml
# deploy/example-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: containix-hello
  annotations:
    # The flake is the source of truth. If its default package declares
    # meta.mainProgram, this is the only annotation you need.
    containix.dev/flake: "nixpkgs#hello"
spec:
  runtimeClassName: containix
  restartPolicy: Never
  containers:
    - name: hello
      # Contents ignored — the rootfs comes from Nix. Any small pullable image
      # works; ghcr.io/atmask/containix-empty is published for this purpose.
      image: "ghcr.io/atmask/containix-empty:0.1.0"
```

```bash
kubectl apply -f deploy/example-pod.yaml
kubectl get pod containix-hello -w
```

## Annotations

| Annotation | Required | Default | Description |
|---|---|---|---|
| `containix.dev/flake` | yes | — | Flake reference to resolve (`nixpkgs#hello`, `github:org/repo`, `github:org/repo#out`). A bare ref builds `packages.<system>.default`. |
| `containix.dev/entrypoint` | no | `meta.mainProgram` | Overrides the executable. Relative → resolved against the flake's store path (`bin/app` → `/nix/store/<hash>/bin/app`); absolute (`/…`) → used as-is. If omitted, the default package's `meta.mainProgram` is used. **If neither is set, pod creation fails with a clear error.** |
| `containix.dev/timeout` | no | `2m` | Max time for `nix build` + closure resolution ([Go duration](https://pkg.go.dev/time#ParseDuration): `30s`, `5m`). Capped at `30m`; invalid values fall back to the default. |

## Verify

```bash
kubectl describe pod containix-hello        # events: scheduling, runtime, errors

# On the node — the shim's own breadcrumbs:
journalctl -u containerd | grep containix
#   [containix] Create called: id=...
#   [containix] Nix flake detected: nixpkgs#hello
#   [containix] store path: /nix/store/...-hello-2.12.3
#   [containix] closure: 5 paths
```

## Troubleshooting

**Pod stuck in `ContainerCreating`** — almost always node prep. Work down the
`journalctl -u containerd | grep containix` output:

| Symptom | Likely cause | Fix |
|---|---|---|
| No `[containix]` lines at all | RuntimeClass `handler` ≠ registered runtime name, or pod not scheduled on a prepared node | Match `handler` to `runtimeName`; check `nodeSelector` / node labels |
| `Create called` but nothing after | `--label`/annotation not reaching the shim, or shim treats it as non-containix | Confirm the `containix.dev/flake` annotation is present on the container |
| Stops after `Nix flake detected` | `nix build` slow, cache unreachable, or bad flake ref | Test `sudo nix build <ref>` on the node; raise `containix.dev/timeout`; check cache reachability |
| `no entrypoint resolved` error | Flake has no `meta.mainProgram` and no `entrypoint` annotation | Add `containix.dev/entrypoint`, or set `meta.mainProgram` in the flake |
| Image pull error | Stub image not pullable | Use a reachable image (any small one works; contents are ignored) |

## Cold starts & binary cache

The first pod for an unseen flake on a node triggers a real `nix build`. Without a
binary cache the node compiles from source, which is slow and can hit
`containix.dev/timeout`. Configure substituters + trusted keys in the node's Nix
config, and consider pre-populating the store on the node image. See
[tutorial 05](./tutorials/05-binary-cache.md).

Node disk: `/nix/store` grows over time. Run `nix-collect-garbage` on a schedule,
mindful of GC roots for live containers.

## Security & policy

containix mirrors runc's options rather than enforcing its own — **cluster policy
is yours to configure**:

- **Non-root / UID policy:** use PodSecurity admission (`runAsNonRoot`, etc.).
  containix passes `Process.User` through untouched; it does not force a UID.
- **Egress:** use NetworkPolicy. containix does not validate or allowlist flake
  refs — restrict what nodes can reach at the network layer.
- **Blast radius:** keep containix on a dedicated, labeled node group.

## Limitations (today)

- **Self-contained binaries only.** Nix binaries resolve their interpreter and
  libraries to `/nix/store`, so they run without an FHS layout — but programs that
  shell out to `/bin/sh`, use `#!/usr/bin/env`, or expect `/usr`/`/lib` aren't
  supported yet.
- **No interactive tooling** in the container (`kubectl exec` lands without
  `sh`/`ls` on `PATH`).
- **NixOS-first.** The manual and managed-cluster paths work but aren't as
  turnkey as the NixOS module.

## Roadmap

Not in v0.1.0, but on the radar: a node-installer DaemonSet / operator to automate
node prep, a Helm chart, shared/pre-warmed `/nix/store` across a node group, and
shim metrics. Contributions welcome — see [CONTRIBUTING](../CONTRIBUTING.md).
