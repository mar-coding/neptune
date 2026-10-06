#!/usr/bin/env bash
# End-to-end smoke test of a running local NEPTUNE stack (hack/local-up.sh):
#   1. SLPA produced communities for the example CommunityConfiguration
#   2. the MIP solver filled the CommunitySchedule with allocations and routing rules
#   3. requests through the dispatcher Service reach the prime-numbers function
#   4. the dispatcher persisted request metrics in TimescaleDB
# Environment: TIMEOUT seconds to wait for each condition (default 300)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TIMEOUT="${TIMEOUT:-300}"

k() { "${ROOT}/hack/dock.sh" kubectl "$@"; }
fail() { echo "FAIL: $*" >&2; exit 1; }

wait_for() {
    local desc="$1"; shift
    local deadline=$(( $(date +%s) + TIMEOUT ))
    until "$@" >/dev/null 2>&1; do
        [ "$(date +%s)" -lt "${deadline}" ] || fail "timed out waiting for ${desc}"
        sleep 5
    done
    echo "ok: ${desc}"
}

has_communities() {
    k -n openfaas-fn get communityconfigurations example-cc -o jsonpath='{.status.generated-communities}' | grep -q community
}
has_allocations() {
    k -n openfaas-fn get communityschedules -o jsonpath='{.items[*].spec.cpu-allocations}' | grep -q prime-numbers
}
function_ready() {
    k -n openfaas-fn get pods -l edgeautoscaler.polimi.it/function-name=prime-numbers --no-headers | grep -qE '([0-9]+)/\1 +Running'
}
# The trailing dot avoids musl (curl image) search-domain resolution issues.
request_ok() {
    k run smoke-probe --rm -i --restart=Never --image=curlimages/curl:8.10.1 -- \
        sh -c 'sleep 2; curl -sf -m10 -o /dev/null http://dispatcher.default.svc.cluster.local./function/openfaas-fn/prime-numbers/prime/1000'
}
has_metrics() {
    [ "$(k -n kube-system exec deploy/metrics-database -- psql -U user -tAc \
        "SELECT count(*) FROM metric WHERE function='prime-numbers' AND status=200")" -gt 0 ]
}

wait_for "SLPA generated communities" has_communities
wait_for "CommunitySchedule has CPU allocations" has_allocations
wait_for "prime-numbers function pod running" function_ready
wait_for "request via dispatcher returns 200" request_ok
wait_for "dispatcher metrics persisted" has_metrics
echo "PASS"
