

# Build the shim binary. GOOS/GOARCH are set explicitly so this works
# from both macOS (cross-compile) and an aarch64 Linux VM (native).
build:
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/

# Build + drop the binary into /var/lib/containix/bin/ + restart containerd.
# Run inside the VM (the install directory is provisioned by the
# containix-dev NixOS module).
install: build
  sudo install -m 755 bin/containerd-shim-containix-v1 /var/lib/containix/bin/
  sudo systemctl restart containerd

# Quick smoke test: install + run a one-shot container via the containix runtime.
# Check `sudo journalctl -u containerd -f | grep containix` to see the shim
# Create handler fire.
test: install
  sudo ctr run --rm \
    --runtime io.containerd.containix.v1 \
    docker.io/library/alpine:latest containix-smoke-test \
    echo "hello from containix"
