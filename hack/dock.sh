#!/usr/bin/env bash
# Run a command inside the NEPTUNE tools container (Go, kubectl, helm, make).
# The repo is mounted at /workspace, the host kubeconfig is reused and the
# Go module/build caches are kept in named volumes.
# Usage: hack/dock.sh kubectl get pods -A
set -euo pipefail

IMAGE="${NEPTUNE_TOOLS_IMAGE:-neptune-tools:latest}"
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

if ! docker image inspect "${IMAGE}" >/dev/null 2>&1; then
    docker build -t "${IMAGE}" "${ROOT}/hack/tools" >&2
fi

# Named volumes are created root-owned; hand the cache to the calling user once.
if ! docker volume inspect neptune-go-cache >/dev/null 2>&1; then
    docker volume create neptune-go-cache >/dev/null
    docker run --rm -v neptune-go-cache:/go-cache "${IMAGE}" chown "$(id -u):$(id -g)" /go-cache
fi

TTY_FLAGS=()
if [ -t 0 ] && [ -t 1 ]; then
    TTY_FLAGS=(-it)
fi

exec docker run --rm "${TTY_FLAGS[@]}" \
    --network host \
    --user "$(id -u):$(id -g)" \
    -e HOME=/tmp \
    -e GOCACHE=/go-cache/build \
    -e GOMODCACHE=/go-cache/mod \
    -e KUBECONFIG=/kube/config \
    -v "${ROOT}:/workspace" \
    -v "${HOME}/.kube:/kube:ro" \
    -v neptune-go-cache:/go-cache \
    -w /workspace \
    "${IMAGE}" "$@"
