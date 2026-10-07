#!/usr/bin/env bash
# Run the Ginkgo e2e suites (pkg/*/pkg/e2e) on a dedicated kind cluster that only
# has the CRDs, RBAC and namespaces installed (same setup as the e2e CI workflow).
# The controllers run in-process with fake SLPA/delay clients, so no NEPTUNE
# component may be deployed on the target cluster.
#
# Unlike `make e2e`, this does not regenerate the CRD manifests.
#
# Environment:
#   CLUSTER_NAME    kind cluster name     (default: neptune-e2e)
#   NODE_IMAGE      kind node image       (default: systemautoscaler/kindest-node:latest)
#   KEEP_CLUSTER=1  keep the cluster after the tests
#   SUITES          packages to run       (default: both e2e suites)
#   E2E_IMAGES      images preloaded into the nodes (default: the instance images used by the suites)
# Extra arguments are passed to go test, e.g. -v or -ginkgo.focus=allocation
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CLUSTER_NAME="${CLUSTER_NAME:-neptune-e2e}"
NODE_IMAGE="${NODE_IMAGE:-systemautoscaler/kindest-node:latest}"
# The suites share the e2e namespace and resources: they must not run in parallel (go test -p 1).
SUITES="${SUITES:-./pkg/system-controller/pkg/e2e/... ./pkg/community-controller/pkg/e2e/...}"
# Pulling these from Docker Hub inside fresh kind nodes can take several minutes,
# longer than the readiness timeouts of the instance management specs.
E2E_IMAGES="${E2E_IMAGES:-systemautoscaler/prime-numbers:dev systemautoscaler/http-metrics:dev}"

k() { "${ROOT}/hack/dock.sh" kubectl --context "kind-${CLUSTER_NAME}" "$@"; }

cleanup() {
    if [ "${KEEP_CLUSTER:-0}" != "1" ]; then
        kind delete cluster --name "${CLUSTER_NAME}" >/dev/null 2>&1 || true
    fi
}

if ! kind get clusters 2>/dev/null | grep -qx "${CLUSTER_NAME}"; then
    echo "==> creating kind cluster ${CLUSTER_NAME}"
    trap cleanup EXIT
    if ! kind create cluster --name "${CLUSTER_NAME}" --config "${ROOT}/config/cluster-conf/kind.conf" \
        --image "${NODE_IMAGE}" --wait 5m; then
        echo "failed to create the e2e cluster. If another kind cluster is running, either stop it" >&2
        echo "(make local-down) or raise fs.inotify.max_user_instances (e.g. to 512)." >&2
        exit 1
    fi
fi

echo "==> preloading instance images"
for image in ${E2E_IMAGES}; do
    docker image inspect "${image}" >/dev/null 2>&1 || docker pull -q "${image}" >/dev/null
    kind load docker-image --name "${CLUSTER_NAME}" "${image}" >/dev/null
done

echo "==> installing CRDs, RBAC and namespaces"
k apply -f config/crd/bases
k apply -f config/openfaas
k apply -f config/cluster-conf/openfaas-fn-namespace.yaml
k apply -f config/cluster-conf/e2e-namespace.yaml
k apply -f config/permissions

echo "==> running e2e suites"
# The suites use the current kubeconfig context: switch it on a private copy so
# the host kubeconfig is left untouched.
# shellcheck disable=SC2086
"${ROOT}/hack/dock.sh" sh -c "cp /kube/config /tmp/kubeconfig && \
    KUBECONFIG=/tmp/kubeconfig kubectl config use-context kind-${CLUSTER_NAME} >/dev/null && \
    KUBECONFIG=/tmp/kubeconfig go test -p 1 -race -count=1 -timeout 30m $* ${SUITES}"
