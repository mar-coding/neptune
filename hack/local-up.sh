#!/usr/bin/env bash
# Bring up the full NEPTUNE stack on a local kind cluster.
# Host requirements: docker and kind. Everything else runs in containers
# (kubectl through hack/dock.sh, Go builds through hack/local/Dockerfile).
#
# Environment:
#   CLUSTER_NAME   kind cluster name                (default: neptune)
#   NODE_IMAGE     kind node image                  (default: systemautoscaler/kindest-node:latest)
#   COMPONENTS     components built from source     (default: system-controller community-controller dispatcher cpu-monitoring)
#   SKIP_BUILD=1   reuse the images already pushed to the local registry
#   WITH_KOSMOS=0  skip the Kosmos node-controller manifests
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-neptune}"
NODE_IMAGE="${NODE_IMAGE:-systemautoscaler/kindest-node:latest}"
COMPONENTS="${COMPONENTS:-system-controller community-controller dispatcher cpu-monitoring}"
REGISTRY_NAME="neptune-registry"
REGISTRY_PORT="5001"
REPO="systemautoscaler"

k() { "${ROOT}/hack/dock.sh" kubectl "$@"; }
log() { echo -e "\n==> $*"; }

create_cluster() {
    if kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
        log "kind cluster ${CLUSTER_NAME} already exists, reusing it"
    else
        log "creating kind cluster ${CLUSTER_NAME}"
        kind create cluster --name "${CLUSTER_NAME}" --config "${ROOT}/config/local/kind.yaml" \
            --image "${NODE_IMAGE}" --wait 5m
    fi
}

start_registry() {
    if [ "$(docker inspect -f '{{.State.Running}}' "${REGISTRY_NAME}" 2>/dev/null)" != "true" ]; then
        log "starting local registry ${REGISTRY_NAME} on 127.0.0.1:${REGISTRY_PORT}"
        docker rm -f "${REGISTRY_NAME}" >/dev/null 2>&1 || true
        docker run -d --restart=always --name "${REGISTRY_NAME}" -p "127.0.0.1:${REGISTRY_PORT}:5000" registry:2 >/dev/null
    fi
    # kind nodes reach the registry by name on the "kind" network (see config/local/kind.yaml)
    docker network connect kind "${REGISTRY_NAME}" >/dev/null 2>&1 || true
}

build_images() {
    if [ "${SKIP_BUILD:-0}" = "1" ]; then
        log "SKIP_BUILD=1, not building images"
        return
    fi
    for c in ${COMPONENTS}; do
        log "building ${REPO}/${c}:dev"
        docker build -f "${ROOT}/hack/local/Dockerfile" --build-arg COMPONENT="${c}" \
            -t "localhost:${REGISTRY_PORT}/${REPO}/${c}:dev" "${ROOT}"
        docker push -q "localhost:${REGISTRY_PORT}/${REPO}/${c}:dev"
    done
    log "building ${REPO}/database:dev"
    docker build -t "localhost:${REGISTRY_PORT}/${REPO}/database:dev" "${ROOT}/config/metric-db"
    docker push -q "localhost:${REGISTRY_PORT}/${REPO}/database:dev"
}

prepare_cluster() {
    log "installing CRDs, namespaces and RBAC"
    k apply -f config/crd/bases
    k apply -f config/openfaas
    k apply -f config/cluster-conf/openfaas-fn-namespace.yaml
    k apply -f config/permissions

    log "allowing workloads on the control plane"
    k taint nodes --all node-role.kubernetes.io/master- >/dev/null 2>&1 || true
    k label nodes "${CLUSTER_NAME}-control-plane" node-role.kubernetes.io/master=true --overwrite

    log "installing metrics-server"
    k apply -f config/local/metrics-server.yaml
}

deploy_database() {
    log "deploying metrics database"
    k apply -f config/deploy/timescale-db.yaml
    k -n kube-system rollout status deployment/metrics-database --timeout=5m
    "${ROOT}/hack/local/seed-delays.sh"
}

deploy_stack() {
    log "deploying NEPTUNE components"
    for f in slpa allocation-algorithm system-controller dispatcher cpu-monitoring; do
        k apply -f "config/deploy/${f}.yaml"
    done
    if [ "${WITH_KOSMOS:-1}" = "1" ]; then
        log "deploying Kosmos node controller"
        for f in monitoring metrics-exposer podscale-controller pod-autoscaler; do
            k apply -f "config/deploy/${f}.yaml"
        done
    fi
    # delay-discovery does not work on kind: latencies are seeded by seed-delays.sh.
    # simple-dispatcher is an alternative to dispatcher and redefines the same Service.

    log "deploying example function and community configuration"
    k apply -f config/deploy/prime-numbers-function.yaml
    k apply -f config/deploy/example-cs.yaml
}

cd "${ROOT}"
create_cluster
start_registry
build_images
prepare_cluster
deploy_database
deploy_stack

log "done. Check with: hack/dock.sh kubectl get pods -A"
