# Run NEPTUNE locally on KinD Cluster

## Requirements
Only two tools on the host:
- Docker (tested with 29.8, cgroup driver `systemd`, cgroup v2)
- Kind (tested with v0.13.0: `curl -Lo ~/.local/bin/kind https://github.com/kubernetes-sigs/kind/releases/download/v0.13.0/kind-linux-amd64 && chmod +x ~/.local/bin/kind`)

Everything else runs in containers:
- `kubectl`, `helm`, `go` and `make` come from the tools image (`hack/tools/Dockerfile`). Run them with `hack/dock.sh <cmd>`, e.g. `hack/dock.sh kubectl get pods -A`.
- The components are compiled inside Docker (`hack/local/Dockerfile`).

## Quick start

```shell
make local-up      # cluster + local registry + build images + deploy everything
make local-smoke   # end-to-end check: communities, schedule, request through dispatcher, metrics
make local-down    # delete cluster and registry (KEEP_REGISTRY=1 keeps the built images)
```

`hack/local-up.sh` options (environment variables):

| Variable | Default | Meaning |
|---|---|---|
| `CLUSTER_NAME` | `neptune` | kind cluster name |
| `NODE_IMAGE` | `systemautoscaler/kindest-node:latest` | node image (Kubernetes v1.18 with in-place vertical scaling) |
| `COMPONENTS` | `system-controller community-controller dispatcher cpu-monitoring` | components built from source |
| `SKIP_BUILD` | `0` | `1` reuses images already in the local registry |
| `WITH_KOSMOS` | `1` | `0` skips the Kosmos node controller |

To redeploy a single component after a code change:

```shell
COMPONENTS=dispatcher ./hack/local-up.sh                  # rebuilds and pushes only the dispatcher image
hack/dock.sh kubectl rollout restart daemonset/dispatcher
```

## How it works

- **Cluster:** `config/local/kind.yaml` has the same topology as `config/cluster-conf/kind.conf` (1 control plane, 3 workers, `InPlacePodVerticalScaling`). It also makes containerd use a local registry (`neptune-registry`, `127.0.0.1:5001`) as a mirror for `docker.io`.
- **Images:** locally built `systemautoscaler/*:dev` images are pushed to that registry. The manifests (and the community-controller Deployment created by the system controller) use `imagePullPolicy: Always`, so they pull the local build, while every other image falls back to Docker Hub.
- **Control plane:** the taint is removed, and the node is labelled `node-role.kubernetes.io/master=true` because community controllers and cpu-monitoring are pinned there.
- **metrics-server:** v0.5.2 (`config/local/metrics-server.yaml`) provides the pod metrics that cpu-monitoring reads.
- **Delays:** delay-discovery does not work on kind. Instead, `hack/local/seed-delays.sh` writes synthetic worker-to-worker latencies into the `ping` table (`BASE_MS + STEP_MS*|i-j|`). Re-run it with other values to shape the topology.
- **Not deployed:** `simple-dispatcher` (an alternative dispatcher that redefines the same Service) and `delay-discovery`.

## Calling a function

The dispatcher serves `/function/<namespace>/<function>/<path>`:

```shell
hack/dock.sh kubectl run probe --rm -i --restart=Never --image=curlimages/curl:8.10.1 -- \
  curl -s http://dispatcher.default.svc.cluster.local./function/openfaas-fn/prime-numbers/prime/1000
```

The trailing dot in the hostname avoids a DNS search-domain problem with musl-based images such as `curlimages/curl`.

## Known limitations

- `kubectl top nodes` reports no data on this node image. Pod metrics, which NEPTUNE uses, work.
- The node image runs Kubernetes v1.18, while the tools image ships kubectl v1.20.

## Manual steps (original guide)

```shell
kind create cluster --config config/cluster-conf/kind.conf --image systemautoscaler/kindest-node
kubectl apply -f config/crd/bases
kubectl apply -f config/cluster-conf/openfaas-fn-namespace.yaml
kubectl apply -f config/openfaas/openfaas-function.yaml
kubectl apply -f config/permissions
kubectl taint node kind-control-plane node-role.kubernetes.io/master:NoSchedule-
kubectl label nodes kind-control-plane node-role.kubernetes.io/master=true --overwrite
kubectl apply -f config/deploy
```

These steps pull the published `:dev` images from Docker Hub instead of building from source.
