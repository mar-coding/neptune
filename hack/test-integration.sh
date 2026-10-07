#!/usr/bin/env bash
# Run the integration tests (build tag "integration") against a throwaway
# TimescaleDB container built from config/metric-db, i.e. the same schema the
# cluster uses. Tests run inside the tools container (hack/dock.sh).
# Extra arguments are passed to go test, e.g. hack/test-integration.sh -run TestMetricsPersistor -v
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGE="neptune-test-db:latest"
CONTAINER="neptune-test-db"
PACKAGES="./pkg/system-controller/pkg/delayclient/... ./pkg/cpu-monitoring/pkg/persistor/... ./pkg/dispatcher/pkg/persistor/..."

cleanup() { docker rm -f "${CONTAINER}" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

echo "==> building test database image"
docker build -q -t "${IMAGE}" "${ROOT}/config/metric-db" >/dev/null

echo "==> starting test database"
docker run -d --name "${CONTAINER}" -e POSTGRES_USER=user -e POSTGRES_PASSWORD=password \
    -p 127.0.0.1::5432 "${IMAGE}" >/dev/null
PORT="$(docker port "${CONTAINER}" 5432/tcp | head -1 | sed 's/.*://')"

# The init scripts run on a temporary server that is restarted afterwards:
# wait until the schema exists and the final server accepts TCP connections.
for _ in $(seq 1 60); do
    if docker exec "${CONTAINER}" psql -h 127.0.0.1 -U user -tAc "SELECT to_regclass('ping')" 2>/dev/null | grep -q ping; then
        break
    fi
    sleep 1
done
docker exec "${CONTAINER}" psql -h 127.0.0.1 -U user -tAc "SELECT to_regclass('ping')" | grep -q ping \
    || { echo "test database did not become ready" >&2; docker logs "${CONTAINER}" >&2; exit 1; }

echo "==> running integration tests against 127.0.0.1:${PORT}"
# shellcheck disable=SC2086
"${ROOT}/hack/dock.sh" env NEPTUNE_TEST_DB_HOST=postgresql://127.0.0.1 NEPTUNE_TEST_DB_PORT="${PORT}" \
    go test -race -count=1 -tags integration "$@" ${PACKAGES}
