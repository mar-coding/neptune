# Testing NEPTUNE

Every test layer runs in Docker. The host needs only `docker` and, for the cluster layers, `kind` (see [run_on_kind.md](run_on_kind.md)).

| Layer | Command | Needs | What it covers |
|---|---|---|---|
| Unit | `make test-unit` | Docker | All packages except e2e. Fake clientsets, listers built on cache indexers, `httptest` servers |
| Integration | `make test-integration` | Docker | SQL delay client and both persistors, against TimescaleDB built from `config/metric-db` (same schema as the cluster) |
| E2E | `make test-e2e` | Docker, kind | Ginkgo suites in `pkg/*/pkg/e2e`: the controllers run in-process against a clean kind cluster, with fake SLPA and delay clients |
| Smoke | `make local-up && make local-smoke` | Docker, kind | The full stack: SLPA communities, MIP schedule, request through the dispatcher, metrics in the DB |

Notes:
- `make test-e2e` creates and deletes its own `neptune-e2e` cluster. With the default `fs.inotify.max_user_instances=128`, two 4-node kind clusters can't run at once, so run `make local-down` (`KEEP_REGISTRY=1` keeps the built images) first.
- Unlike `make e2e`, it does not regenerate the CRD manifests. It runs the suites one at a time (`go test -p 1`), because both use the `e2e` namespace and the same resources.
- It preloads the instance images (`E2E_IMAGES`) into the kind nodes. Pulling `prime-numbers` from Docker Hub inside fresh nodes took about 6 minutes, longer than the readiness timeouts.
- Run a single suite or spec with `SUITES=./pkg/community-controller/pkg/e2e/... hack/test-e2e.sh -v -ginkgo.focus=allocation`. `KEEP_CLUSTER=1` keeps the cluster so you can inspect it.
- The root `make test` only runs the per-component Makefiles, so it skips the shared packages (`pkg/queue`, `pkg/labels`, `pkg/apiutils`, `pkg/metrics`). Use `make test-unit`.
- Integration tests use the `integration` build tag and are skipped unless `NEPTUNE_TEST_DB_PORT` is set. `hack/test-integration.sh` sets it. Pass `go test` flags through, e.g. `hack/test-integration.sh -v -run TestMetricsPersistor`.

## Instance management coverage

Instance management means placing, creating, replacing and removing function instances. It is covered at three levels:
- **Unit:** `pkg/community-controller/pkg/controller/lifecycle_test.go` covers the reconcile loop: scale out and in, moves with ready-before-delete handover, duplicates, removed functions, GPU vs CPU instances, and `OutOfcpu` cleanup.
- **Unit:** `scheduler_flow_test.go` covers the solver contract (input built from cluster state, output written to the schedule), solver failures, and the events that trigger rescheduling.
- **Unit:** `pkg/system-controller/pkg/controller/flow_test.go` covers communities, then schedules and community controllers.
- **E2E:** the community-controller suite ("When the allocation of a function changes") runs real pods on kind through scale out, scale in and a move. It checks that surviving instances are not recreated.

Vertical scaling of instances (Kosmos) is not part of this repository and is only exercised by the smoke test.

## Conventions

- Unit tests are table-driven with `testify/require`, in the package under test, so unexported code can be tested.
- For Kubernetes objects, build listers on `cache.NewIndexer` (see `pkg/dispatcher/pkg/controller/sync_test.go`), or use the generated fake clientsets.
- Never bind fixed ports in tests. Use `httptest.NewServer`.
- Known bugs have a test that is skipped with a `known bug:` message. Fixing the bug means removing the skip. Find them with `grep -rn 'known bug' pkg`.

## Known bugs pinned by skipped tests

| Test | Problem |
|---|---|
| `pkg/apiutils` `TestGetNodeDelaysIgnoresUnknownNodes` | delays of nodes outside the requested list (e.g. the control plane) are written to row/column 0 of the delay matrix |
| `pkg/dispatcher/pkg/controller` `TestSyncRoutingRulesIgnoresOtherSources` | when the local node is not a source in the routing rules, the dispatcher applies another node's rules |
| `pkg/community-controller/pkg/controller` `TestSyncReplacesTerminatingInstances` | a running instance that is being deleted still counts as live, so no replacement is created on its node |
| `pkg/community-controller/pkg/controller` `TestSyncDeletesOutOfCPUInstancesOnce` | an `OutOfcpu` instance on an unallocated node is deleted twice: the second delete fails the whole sync |
| `pkg/community-controller/pkg/controller` `TestSyncReplacesOutOfCPUInstances` | an `OutOfcpu` instance on an allocated node counts as live and is deleted without a replacement, leaving no instance until the next sync |
| `pkg/community-controller/pkg/controller` `TestRunSchedulerFunctionWithoutLabels` | a Function without `spec.labels` panics the community controller (nil dereference in `NewSchedulingInput`) |
