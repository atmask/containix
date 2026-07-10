# Running containix on Kubernetes

An operator-focused guide to getting containix working on a real cluster. If you
want the step-by-step teaching version, see
[tutorials/04-kubernetes.md](./tutorials/04-kubernetes.md); this page is the
condensed "get it running" path.

> **Status (v0.1.0):** experimental. Validated end-to-end on NixOS — both the
> system containerd (via `ctr`) and single-node k3s (via `kubectl`), containerd
> 2.x. Everything here is `kubectl apply` + node prep — there is **no Helm chart,
> operator, or node-installer DaemonSet yet** (see [Roadmap](#roadmap)).

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

Three things have to line up on **every node that runs containix pods**:

1. The **shim binary** is on containerd's `PATH`, and containerd has the runtime
   registered (`runtimes.containix`).
2. That runtime **passes `containix.dev/*` annotations through to the OCI spec**.
   CRI drops custom annotations by default, so without this the shim never sees
   the flake reference and the pod silently runs nothing — the most common
   "applied it, but the flake didn't run" cause. The NixOS module configures this
   for you; the manual path sets it explicitly (below).
3. A **Nix daemon** is available so the shim can resolve and build flakes.

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

Every containix node needs the same three things, on any distro:

1. **A Nix daemon** (multi-user install provides the daemon):
   ```bash
   curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh
   ```
2. **The shim binary** on containerd's `PATH` (`/usr/local/bin` is fine):
   ```bash
   # build it:  nix build github:atmask/containix#default    (result/bin/…)
   # or grab the release asset for your arch:
   #   https://github.com/atmask/containix/releases
   sudo cp containerd-shim-containix-v1 /usr/local/bin/
   ```
3. **The runtime registered** in containerd's CRI config — see the next section.

The distros differ only in *how* those land on the node (below). NixOS automates
all three; everything else you do by hand or bake into a node image.

### The runtime block (same on every distro)

You add one runtime block to containerd's CRI config. It's identical everywhere —
only the containerd **config version** changes the plugin key:

```toml
# config version 3 — containerd 2.x (EKS, GKE, kubeadm, k3s on recent nodes)
[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
  pod_annotations = ["containix.dev/*"]        # REQUIRED — CRI drops custom
  container_annotations = ["containix.dev/*"]  # annotations without these, so the
                                               # shim never sees the flake ref and
                                               # the pod silently runs nothing.
```

```toml
# config version 2 — containerd 1.x (2.x still accepts it)
[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
  pod_annotations = ["containix.dev/*"]
  container_annotations = ["containix.dev/*"]
```

Check your version with `grep -E '^version' /etc/containerd/config.toml` (or the
file named in `systemctl cat containerd`). The `pod_annotations` /
`container_annotations` globs are the part everyone forgets — they are mandatory.
If your cluster uses the systemd cgroup driver, also add `SystemdCgroup = true`
under a `.options` sub-table of the runtime.

### Getting the block onto your nodes

**NixOS — easiest, and the validated path.** The module does all three requirements
(runtime + passthrough, containerd enabled, shim/`nix`/`runc` on PATH):

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

The `RuntimeClass` `handler` must equal `services.containix.runtimeName` (default
`containix`). After `nixos-rebuild switch`, smoke-test the node directly:

```bash
sudo ctr image pull docker.io/library/busybox:latest
sudo ctr run --rm --runtime io.containerd.containix.v1 \
  --label containix.dev/flake="nixpkgs#hello" \
  docker.io/library/busybox:latest containix-check    # → Hello, world!
```

(Heads-up: on NixOS `containerd config dump` reads the *default* config path, not
the daemon's `--config` store path, so it can misleadingly show no containix
runtime — trust the `ctr` test.)

**Self-managed containerd — kubeadm, bare metal, self-managed cloud node AMIs.**
Append the [runtime block](#the-runtime-block-same-on-every-distro) to
`/etc/containerd/config.toml` (or a drop-in under `/etc/containerd/config.toml.d/`),
then `sudo systemctl restart containerd`.

**k3s.** k3s *regenerates* its containerd config on start, so add a template rather
than editing the rendered file. Modern k3s reads `config-v3.toml.tmpl`; begin it
with `{{ template "base" . }}` to inherit the base config, then append the block:

```toml
# /var/lib/rancher/k3s/agent/etc/containerd/config-v3.toml.tmpl
{{ template "base" . }}

[plugins.'io.containerd.cri.v1.runtime'.containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
  pod_annotations = ["containix.dev/*"]
  container_annotations = ["containix.dev/*"]
```

`systemctl restart k3s`, then confirm it rendered: `grep -A3 runtimes.containix
/var/lib/rancher/k3s/agent/etc/containerd/config.toml`. The shim/`nix`/`runc` must
also be on k3s's service `PATH` (`systemd.services.k3s.path` on NixOS) so its
embedded containerd can exec the shim.

**Managed node groups — EKS, GKE, and similar.** You can't edit a running managed
node's config, so apply the three requirements at **node bootstrap**: either bake
Nix + the shim + the runtime block into a custom node image / launch-template
user-data, or run a privileged **DaemonSet** that installs them and restarts
containerd. Pre-seeding the Nix store with common closures in the image also cuts
first-pod cold starts. This is the heaviest path — there's no node-installer yet.

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
# Primary success signal — the flake's own output:
kubectl logs containix-hello                # e.g. "Hello, world!"

kubectl describe pod containix-hello        # events: scheduling, runtime, errors

# On the node — the shim's breadcrumbs, under whichever unit owns containerd:
#   system containerd:  journalctl -u containerd | grep containix
#   k3s (embedded):     journalctl -u k3s        | grep containix
#   [containix] Nix flake detected: nixpkgs#hello
#   [containix] store path: /nix/store/...-hello-2.12.3
# (Under k3s the shim's stdout may not reach the journal — kubectl logs is the
#  more reliable signal.)
```

## Troubleshooting

**Pod stuck in `ContainerCreating`** — almost always node prep. Work down the
`journalctl -u containerd | grep containix` output:

| Symptom | Likely cause | Fix |
|---|---|---|
| No `[containix]` lines at all | RuntimeClass `handler` ≠ registered runtime name, or pod not scheduled on a prepared node | Match `handler` to `runtimeName`; check `nodeSelector` / node labels |
| `Create called` but no `Nix flake detected` | Annotation not reaching the shim — CRI annotation passthrough not configured for this runtime | Add `pod_annotations = ["containix.dev/*"]` to the `runtimes.containix` block and restart containerd |
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
