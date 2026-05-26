

build:
  GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -o bin/containerd-shim-containix-v1 ./cmd/containerd-shim-containix-v1/
