# Tutorial 4: Kubernetes

Deploy a Nix flake as a Kubernetes Pod on a real cluster. This tutorial bridges the containerd-level work from Tutorials 1-3 into the Kubernetes control plane.

## Goal

- Create a RuntimeClass for containix
- Deploy the shim on cluster nodes (dedicated node group)
- Run a Pod that references a Nix flake via annotations
- Verify end-to-end: `kubectl apply` → Nix-backed container running

## Prerequisites

- A Kubernetes cluster where you control the nodes (EKS, k3s, or kubeadm)
- The containix shim binary from Tutorial 1-2
- Nix daemon available on at least one node

## Approach: Dedicated node group

Don't install containix on all nodes. Use a dedicated node group with a label, and use `nodeSelector` to schedule containix pods there. This keeps the blast radius small.

## Step 1: Prepare the node

On each node in the containix node group:

```bash
# Install Nix (if not already present)
curl --proto '=https' --tlsv1.2 -sSf -L https://install.determinate.systems/nix | sh

# Copy the shim binary
sudo cp containerd-shim-containix-v1 /usr/local/bin/

# Label the node
kubectl label node <node-name> containix.dev/runtime=true
```

### Configure containerd — standard distribution

Append to `/etc/containerd/config.toml`:

```bash
sudo tee -a /etc/containerd/config.toml <<EOF

[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix.options]
    SystemdCgroup = true
EOF

sudo systemctl restart containerd
```

### Configure containerd — k3s

k3s regenerates its containerd config (`/var/lib/rancher/k3s/agent/etc/containerd/config.toml`) on every start, so direct edits get overwritten. Use the template extension:

```bash
# Seed the template from the current rendered config the first time
sudo cp /var/lib/rancher/k3s/agent/etc/containerd/config.toml \
        /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl

# Append the containix runtime block
sudo tee -a /var/lib/rancher/k3s/agent/etc/containerd/config.toml.tmpl <<'EOF'

[plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix]
  runtime_type = "io.containerd.containix.v1"
  [plugins."io.containerd.grpc.v1.cri".containerd.runtimes.containix.options]
    SystemdCgroup = true
EOF

sudo systemctl restart k3s
```

After restart, verify:

```bash
grep -A 3 containix /var/lib/rancher/k3s/agent/etc/containerd/config.toml
```

The block should be present in the rendered file.

### EKS / GKE / managed Kubernetes

Bake the shim binary, Nix installation, and config changes into a custom AMI or `cloud-init` script for the dedicated node group. See the EKS-specific notes below.

## Step 2: Create the RuntimeClass

```yaml
# manifests/runtime-class.yaml
apiVersion: node.k8s.io/v1
kind: RuntimeClass
metadata:
  name: containix
handler: containix
scheduling:
  nodeSelector:
    containix.dev/runtime: "true"
```

```bash
kubectl apply -f manifests/runtime-class.yaml
```

The `scheduling.nodeSelector` ensures pods using this RuntimeClass are only scheduled on nodes that have the shim installed.

## Step 3: Build the stub image

Build a minimal stub image and push it to your registry:

```dockerfile
# Dockerfile.empty
FROM scratch
COPY --from=busybox /bin/true /true
```

```bash
docker build -f Dockerfile.empty -t <your-registry>/containix-empty:1.0.0 .
docker push <your-registry>/containix-empty:1.0.0
```

Or build it with Nix:

```nix
# stub-image/flake.nix
{
  inputs.nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

  outputs = { nixpkgs, ... }:
    let
      system = "x86_64-linux";
      pkgs = nixpkgs.legacyPackages.${system};
    in {
      packages.${system}.default = pkgs.dockerTools.buildImage {
        name = "containix-empty";
        tag = "1.0.0";
        copyToRoot = pkgs.writeTextDir "/.containix" "stub";
      };
    };
}
```

## Step 4: Deploy a Pod

```yaml
# manifests/test-pod.yaml
apiVersion: v1
kind: Pod
metadata:
  name: containix-hello
  annotations:
    containix.dev/flake: "github:NixOS/nixpkgs#hello"
    containix.dev/entrypoint: "hello"
spec:
  runtimeClassName: containix
  containers:
  - name: hello
    image: "<your-registry>/containix-empty:1.0.0"
    command: ["sleep", "infinity"]  # overridden by shim
  restartPolicy: Never
```

```bash
kubectl apply -f manifests/test-pod.yaml
```

## Step 5: Verify

```bash
# Watch the pod
kubectl get pod containix-hello -w

# Check events for scheduling and runtime details
kubectl describe pod containix-hello

# Check logs on the node
ssh <node> 'sudo journalctl -u containerd --since "5 minutes ago" | grep containix'

# Exec into the pod (if the closure includes a shell)
kubectl exec -it containix-hello -- /nix/store/xxx-bash-5.2/bin/bash
```

## Step 6: Deploy a real workload

A simple web server:

```yaml
# manifests/test-server.yaml
apiVersion: v1
kind: Pod
metadata:
  name: containix-server
  annotations:
    containix.dev/flake: "github:myorg/myapp#default"
    containix.dev/entrypoint: "/nix/store/xxx-myapp/bin/myapp"
spec:
  runtimeClassName: containix
  containers:
  - name: app
    image: "<your-registry>/containix-empty:1.0.0"
    ports:
    - containerPort: 8080
---
apiVersion: v1
kind: Service
metadata:
  name: containix-server
spec:
  selector:
    # Note: you'll need to add labels to the pod
  ports:
  - port: 80
    targetPort: 8080
```

## EKS-specific notes

### Custom AMI

For EKS, the cleanest approach is a custom AMI based on the EKS-optimized AMI:

```bash
# In your AMI build (Packer or similar):
# 1. Install Nix
# 2. Copy containerd-shim-containix-v1 to /usr/local/bin/
# 3. Add containerd runtime config
# 4. Pre-populate the Nix store with common packages (optional, for faster cold starts)
```

### Managed node group with launch template

```hcl
# terraform
resource "aws_eks_node_group" "containix" {
  cluster_name    = aws_eks_cluster.main.name
  node_group_name = "containix"
  node_role_arn   = aws_iam_role.node.arn
  subnet_ids      = var.subnet_ids

  labels = {
    "containix.dev/runtime" = "true"
  }

  launch_template {
    id      = aws_launch_template.containix.id
    version = "$Latest"
  }

  scaling_config {
    desired_size = 1
    max_size     = 3
    min_size     = 1
  }
}
```

## What you've built

A Kubernetes-native deployment path for Nix flakes:
- RuntimeClass routes pods to the containix shim
- Annotations carry the flake reference through the CRI to the shim
- The shim resolves, builds, and mounts — Kubernetes manages the lifecycle

## Current limitations

- **Entrypoint requires full store paths**: Ugly and fragile. An admission webhook or init container could resolve these automatically.
- **No health checks on Nix operations**: If `nix build` hangs, the pod hangs in `ContainerCreating` forever. We need timeouts.
- **Binary cache auth**: If your flake needs a private cache, the node's Nix config needs credentials. Not handled yet.
- **Cold start**: First pod on a fresh node is slow. Tutorial 5 addresses this.

## Next

[Tutorial 5: Binary Cache](./05-binary-cache.md) — optimize cold starts and configure binary cache integration.
