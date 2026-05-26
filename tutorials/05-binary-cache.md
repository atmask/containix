# Tutorial 5: Binary Cache

The biggest practical problem with containix is cold-start latency. The first pod on a fresh node runs `nix build`, which fetches the entire closure from a binary cache. This tutorial covers how to make that fast.

## Goal

- Configure nodes to pull from a binary cache
- Pre-warm the node's Nix store for near-instant pod starts
- Set up a DaemonSet that keeps the store populated
- Measure and optimize fetch times

## The cold-start problem

```
First pod on fresh node:
  nix build → eval flake → fetch 200 store paths from cache → mount rootfs
  Time: 30-120 seconds (depends on closure size and network)

Second pod with same/overlapping deps:
  nix build → eval flake → all paths cached locally → mount rootfs
  Time: 2-5 seconds
```

The Nix store is content-addressed. Once a store path is on the node, it's there for any container that needs it. The problem is the first fetch.

## Step 1: Configure a binary cache

### Option A: Cachix (easiest)

```bash
# On each node
nix-env -iA cachix -f https://cachix.org/api/v1/install
cachix use your-cache-name
```

Or add to `/etc/nix/nix.conf`:

```
substituters = https://cache.nixos.org https://your-cache.cachix.org
trusted-public-keys = cache.nixos.org-1:6NCHdD59X431o0gWypbMrAURkbJ16ZPMQFGspcDShjY= your-cache.cachix.org-1:XXXX=
```

### Option B: Attic (self-hosted)

If you want to keep everything in your own infrastructure:

```bash
# Deploy Attic server (can run in the same cluster)
# Configure nodes to use it as a substituter
```

### Option C: S3-backed Nix cache

Store narinfo and nar files in S3. Nix supports this natively:

```
substituters = s3://my-nix-cache?region=us-east-1
```

This pairs well with EKS — nodes already have IAM roles that can read S3.

## Step 2: Push your flake outputs to the cache

In CI, after building:

```bash
# Cachix
nix build .#default
cachix push your-cache result

# S3
nix copy --to s3://my-nix-cache result
```

## Step 3: Pre-warm with a DaemonSet

A DaemonSet that runs on containix nodes and pre-fetches common closures:

```yaml
# manifests/pre-warmer.yaml
apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: containix-prewarmer
  namespace: kube-system
spec:
  selector:
    matchLabels:
      app: containix-prewarmer
  template:
    metadata:
      labels:
        app: containix-prewarmer
    spec:
      nodeSelector:
        containix.dev/runtime: "true"
      tolerations:
      - operator: Exists
      hostPID: true
      containers:
      - name: prewarmer
        image: nixos/nix:latest
        command:
        - /bin/sh
        - -c
        - |
          # Pre-fetch store paths for known workloads
          # This list can be managed via a ConfigMap
          while read flakeref; do
            echo "Pre-warming: $flakeref"
            nix build --no-link "$flakeref" 2>&1 || true
          done < /config/flakes.txt

          echo "Pre-warm complete. Sleeping."
          sleep infinity
        volumeMounts:
        - name: nix-store
          mountPath: /nix
        - name: config
          mountPath: /config
      volumes:
      - name: nix-store
        hostPath:
          path: /nix
      - name: config
        configMap:
          name: containix-prewarmer-config
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: containix-prewarmer-config
  namespace: kube-system
data:
  flakes.txt: |
    github:myorg/service-a#default
    github:myorg/service-b#default
    github:myorg/shared-libs#runtime
```

## Step 4: Measure performance

Add timing to the shim's Create method:

```go
func (s *Service) Create(ctx context.Context, req *taskAPI.CreateTaskRequest) (*taskAPI.CreateTaskResponse, error) {
	cfg, err := nix.FromBundle(req.Bundle)
	if err != nil {
		return nil, err
	}

	if cfg != nil {
		start := time.Now()

		storePath, err := nix.Build(cfg.FlakeRef)
		if err != nil {
			return nil, err
		}
		buildDuration := time.Since(start)

		closure, err := nix.Closure(storePath)
		if err != nil {
			return nil, err
		}

		if err := nix.PrepareRootfs(req.Bundle, cfg, closure); err != nil {
			return nil, err
		}

		log.Printf("[containix] flake=%s paths=%d build_ms=%d",
			cfg.FlakeRef, len(closure), buildDuration.Milliseconds())
	}

	return s.TTRPCTaskService.Create(ctx, req)
}
```

### Expected performance

| Scenario | Time |
|----------|------|
| All paths cached locally (pre-warmed) | 1-3s |
| Partial cache hit (shared deps exist) | 5-15s |
| Cold start, fast binary cache (same region S3) | 15-45s |
| Cold start, remote cache (cachix, cross-region) | 30-120s |

## Step 5: Nix store garbage collection

The Nix store on each node grows over time. You need a GC policy that doesn't delete paths used by running pods.

```bash
# Simple approach: keep paths referenced in the last 7 days
nix-collect-garbage --delete-older-than 7d
```

A safer approach for containix: maintain a GC roots directory that the shim writes to when mounting paths, and removes on container delete.

```go
// In Create: register GC roots
func registerGCRoots(containerID string, storePaths []string) error {
	rootsDir := filepath.Join("/nix/var/nix/gcroots/containix", containerID)
	if err := os.MkdirAll(rootsDir, 0755); err != nil {
		return err
	}
	for _, sp := range storePaths {
		link := filepath.Join(rootsDir, filepath.Base(sp))
		if err := os.Symlink(sp, link); err != nil {
			return err
		}
	}
	return nil
}

// In Delete: remove GC roots
func removeGCRoots(containerID string) error {
	return os.RemoveAll(filepath.Join("/nix/var/nix/gcroots/containix", containerID))
}
```

## What you've built

- Binary cache integration for fast store path fetches
- A DaemonSet pre-warmer for near-instant cold starts
- Performance instrumentation in the shim
- GC roots management to prevent store path eviction while pods are running

## Next

[Tutorial 6: Production Hardening](./06-production-hardening.md) — health checks, metrics, error handling, and operational concerns.
