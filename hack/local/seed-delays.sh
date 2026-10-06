#!/usr/bin/env bash
# Seed the `ping` table with synthetic node-to-node latencies.
# delay-discovery does not work on kind, so the system controller would see an
# all-zero delay matrix. Latency between worker i and j is BASE_MS + STEP_MS*|i-j|.
# Control-plane nodes are skipped: the system controller ignores them, and
# unknown node names would be mapped onto row 0 of the delay matrix.
#
# Environment: BASE_MS (default 5), STEP_MS (default 10)
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
BASE_MS="${BASE_MS:-5}"
STEP_MS="${STEP_MS:-10}"

k() { "${ROOT}/hack/dock.sh" kubectl "$@"; }

POD="$(k -n kube-system get pods -l app=metrics-database -o jsonpath='{.items[0].metadata.name}')"
psql() { k -n kube-system exec "${POD}" -- psql -v ON_ERROR_STOP=1 -U user -q "$@"; }

echo "waiting for the ping table in ${POD}"
for _ in $(seq 1 60); do
    if psql -tAc "SELECT to_regclass('ping')" 2>/dev/null | grep -q ping; then
        break
    fi
    sleep 2
done

mapfile -t NODES < <(k get nodes -l '!node-role.kubernetes.io/master' -o jsonpath='{range .items[*]}{.metadata.name}{"\n"}{end}' | sort)

SQL="DELETE FROM ping;"
n=0
for i in "${!NODES[@]}"; do
    for j in "${!NODES[@]}"; do
        d=$(( i > j ? i - j : j - i ))
        lat=$(( BASE_MS + STEP_MS * d ))
        # (timestamp, from_node) is the primary key: give every row its own timestamp
        SQL+="INSERT INTO ping VALUES (now() - interval '${n} milliseconds', '${NODES[$i]}', '${NODES[$j]}', ${lat}, ${lat}, ${lat});"
        n=$(( n + 1 ))
    done
done

echo "seeding ${n} latencies for nodes: ${NODES[*]}"
psql -c "${SQL}"
