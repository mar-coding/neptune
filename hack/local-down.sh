#!/usr/bin/env bash
# Tear down the local NEPTUNE environment created by hack/local-up.sh.
# KEEP_REGISTRY=1 keeps the local registry (and its images) for the next run.
set -euo pipefail

CLUSTER_NAME="${CLUSTER_NAME:-neptune}"

kind delete cluster --name "${CLUSTER_NAME}"
if [ "${KEEP_REGISTRY:-0}" != "1" ]; then
    docker rm -f neptune-registry >/dev/null 2>&1 || true
fi
