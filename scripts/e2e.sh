#!/usr/bin/env bash
# End-to-end smoke tests for the containix shim against a live containerd.
#
# Assumes containerd is running with containerd-shim-containix-v1, nix and
# runc on its PATH. Run as root (ctr talks to containerd's root socket):
#
#   sudo env "PATH=$PATH" scripts/e2e.sh
#
# Point at a non-default containerd with CONTAINERD_ADDRESS (ctr honours it).
# Used by `just e2e` on the dev VM and by the e2e job in CI.

set -euo pipefail

RUNTIME=io.containerd.containix.v1
IMAGE=docker.io/library/alpine:latest
PREFIX="containix-e2e-$$"
failures=0

pass() { echo "PASS: $*"; }
fail() {
  echo "FAIL: $*"
  failures=$((failures + 1))
}

# Run a container through the containix runtime. Extra args go to `ctr run`.
crun() { timeout 300 ctr run --rm --runtime "$RUNTIME" "$@"; }

cleanup() {
  local c
  for c in $(ctr container ls -q | grep "^${PREFIX}" || true); do
    ctr task kill -s SIGKILL "$c" >/dev/null 2>&1 || true
    ctr task delete "$c" >/dev/null 2>&1 || true
    ctr container delete "$c" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

ctr image pull "$IMAGE" >/dev/null

echo "== one-shot flake container"
out=$(crun --label containix.dev/flake="nixpkgs#hello" \
  --label containix.dev/entrypoint="bin/hello" \
  "$IMAGE" "${PREFIX}-hello" 2>&1) || true
if [[ "$out" == *"Hello, world!"* ]]; then pass "nixpkgs#hello ran"; else fail "nixpkgs#hello: $out"; fi

echo "== passthrough (no containix label) runs as plain runc"
out=$(crun "$IMAGE" "${PREFIX}-plain" echo passthrough-ok 2>&1) || true
if [[ "$out" == *"passthrough-ok"* ]]; then pass "passthrough container ran"; else fail "passthrough: $out"; fi

echo "== bad flake fails cleanly"
if out=$(crun --label containix.dev/flake="nixpkgs#containix-e2e-no-such-package" \
  "$IMAGE" "${PREFIX}-bad" 2>&1); then
  fail "bad flake unexpectedly succeeded: $out"
elif [[ "$out" == *"nix build"* ]]; then
  pass "bad flake rejected with nix build error"
else
  fail "bad flake failed with unexpected error: $out"
fi
ctr container delete "${PREFIX}-bad" >/dev/null 2>&1 || true

echo "== long-running lifecycle: run, exec, kill, delete"
name="${PREFIX}-redis"
redis=$(nix build --no-link --print-out-paths "nixpkgs#redis" | head -1)
ctr run -d --runtime "$RUNTIME" \
  --label containix.dev/flake="nixpkgs#redis" \
  --label containix.dev/entrypoint="bin/redis-server" \
  "$IMAGE" "$name" >/dev/null
for _ in $(seq 1 60); do
  ctr task ls | grep -q "^${name} .*RUNNING" && break
  sleep 2
done
if ctr task ls | grep -q "^${name} .*RUNNING"; then pass "task running"; else fail "task never reached RUNNING"; fi

pong=""
for _ in $(seq 1 15); do
  pong=$(ctr task exec --exec-id ping "$name" "$redis/bin/redis-cli" ping 2>&1) && break
  sleep 1
done
if [[ "$pong" == "PONG" ]]; then pass "exec into task"; else fail "exec: $pong"; fi

ctr task kill -s SIGTERM "$name"
for _ in $(seq 1 15); do
  ctr task ls | grep -q "^${name} .*STOPPED" && break
  sleep 1
done
if ctr task ls | grep -q "^${name} .*STOPPED"; then pass "task stopped on SIGTERM"; else fail "task did not stop"; fi

ctr task delete "$name" >/dev/null
ctr container delete "$name"
pass "task and container deleted"

echo "== nothing left behind"
if ctr container ls -q | grep -q "^${PREFIX}"; then
  fail "leftover containers: $(ctr container ls -q | grep "^${PREFIX}" | tr '\n' ' ')"
else
  pass "no leftover containers"
fi

echo
if ((failures)); then
  echo "${failures} check(s) failed"
  exit 1
fi
echo "all checks passed"
