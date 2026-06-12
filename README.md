# MyceDrive

MyceDrive migrates **stateful pods** between Kubernetes nodes while preserving the application's live state. It combines two independently toggleable mechanisms:

- **Process memory migration (DMTCP)** — checkpoints the container's processes, memory, and open TCP connections, and restores them on the destination node.
- **Volume migration (OverlayFS)** — snapshots the pod's volume as read-only overlay layers and transfers them *before* the downtime window, so only the last small layer moves while the pod is down.

## Architecture

- **Operator (Migration Coordinator)** — deployed once per cluster. Watches the `MigratableWorkload` and `Migration` CRDs, tracks registered pods, and drives the migration sequence through the Kubernetes API. Serves the REST API the agents use and a web dashboard.
- **Execution Agent (EA, `go-agent`)** — runs inside every application container. Wraps the entry point with `dmtcp_launch`, executes the preStop checkpoint flow, manages overlay layers, and streams checkpoints to the destination.
- **DMTCP sidecar** — a lightweight container per pod running `dmtcp_coordinator`, sharing the DMTCP binaries and checkpoint dir over an `emptyDir` volume.

## Requirements & Limitations

Read this before deploying:

- **Kubernetes** ≥ 1.26 with `kubectl` and Helm v3. The DMTCP sidecar and the
  overlay mounts require **Linux nodes**; volume migration needs OverlayFS
  support in the node kernel (any modern distribution kernel qualifies).
- **Process migration (DMTCP)** checkpoints userspace processes. It does not
  cover GPU state, and applications using esoteric kernel interfaces
  (io_uring, eBPF maps, inotify watches across restore) may not restore
  cleanly. Open TCP connections are preserved; UDP state is not.
- **Volume migration (overlay)** checkpoints the layered volume managed by the
  Execution Agent. It does not migrate PVCs managed by external CSI drivers —
  if a pod also mounts a PVC, that data moves (or not) per your storage class,
  and can diverge from the migrated overlay state.
- The Migration Coordinator REST API is **unauthenticated, in-cluster only**.
  Do not expose the operator Service publicly (see [SECURITY.md](SECURITY.md)).
- Migration moves a pod **between nodes of one cluster**. Cross-cluster
  migration is out of scope for now.

## Installing the Operator

```sh
helm repo add mycedrive https://paulosouzajr.github.io/mycedrive-k8s
helm repo update
helm install mycedrive-operator mycedrive/mycedrive-operator \
  --namespace mig-ready --create-namespace --version 0.1.1
```

Pin `--version` to a released chart; omit it only if you want the latest.
Installing into `mig-ready` matches the defaults used by the example
scenarios and `make-migratable.sh` (`MIGR_COOR=mycedrive.mig-ready.svc.cluster.local`);
if you pick another namespace, set `MIGR_COOR` accordingly in your workloads.

Or from the local chart:

```sh
helm install mycedrive-operator deployment/operator
```

This installs the CRDs, RBAC, the operator Deployment (image `mycedrive/operator`), and two Services: the operator API and a legacy alias `mycedrive` (port 80 → container 8080).

## Making a StatefulSet Migratable

**1. Image:** make your entrypoint start the EA before the application. The
binaries arrive in the pod automatically — the DMTCP init container copies
`go-agent` and `end_container` (plus the DMTCP tools) into the shared
`/dmtcp/bin` volume, so the entrypoint only needs:

```sh
/dmtcp/bin/go-agent || true                       # register + (on a target) restore
exec /dmtcp/bin/dmtcp_launch -j ${START_UP}       # fresh start under DMTCP
```

See `examples/mosquitto_d/docker-entrypoint.sh` for the complete pattern,
including the `.restored` marker check. Alternatively embed the binary:
`COPY --from=mycedrive/go-agent:dev /go-agent /usr/local/bin/go-agent`.

**2. Workload:** patch the StatefulSet with the sidecar, shared volume, env vars, and preStop hook:

```sh
./scripts/make-migratable.sh -n NAMESPACE -s STATEFULSET -u "your startup command"
```

Use `--dry-run` to print the patch without applying it. See [docs/making-statefulsets-migratable.md](docs/making-statefulsets-migratable.md) for details and limitations.

**3. (Recommended) Declare the workload** so you can tune migration behavior:

```yaml
apiVersion: mycedrive.io/v1alpha1
kind: MigratableWorkload
metadata:
  name: my-app          # same namespace as the workload
spec:
  workloadRef:
    kind: StatefulSet    # primary target; Deployment also supported
    name: my-app
  processMigration: true # DMTCP memory/socket checkpoint
  volumeMigration: true  # OverlayFS volume layer checkpointing
  preSyncRounds: 1       # overlay rounds transferred before downtime
```

`processMigration` and `volumeMigration` can be enabled independently. The same toggles exist on the agent as env vars (`ENABLE_PROCESS_MIGRATION`, `ENABLE_VOLUME_MIGRATION`, both default `true`).

## Migrating a Pod

Create a `Migration` (and `kubectl get mig` to watch progress):

```yaml
apiVersion: mycedrive.io/v1alpha1
kind: Migration
metadata:
  name: move-my-app
spec:
  workloadName: my-app
  sourceNode: worker-01
  targetNode: worker-02
```

Phases: `Pending → Syncing → Checkpointing → Transferring → Restoring → Completed` (or `Failed`). The legacy REST trigger also works and auto-creates the CRs:

```sh
curl -X POST http://<operator-service>/migrate \
  -d '{"workload":"my-app","sourceNode":"worker-01","targetNode":"worker-02"}'
```

## Try It: Migration Scenarios

`examples/scenarios/` ships three ready-made test scenarios (both mechanisms,
process-only, volume-only) built around a Mosquitto broker whose state is a
retained MQTT message, plus a script that deploys, migrates and verifies
end to end:

```sh
./examples/run-scenario.sh both        # or: process-only | volume-only
```

See [examples/scenarios/README.md](examples/scenarios/README.md) for the
manual walkthrough and troubleshooting.

## Dashboard

```sh
kubectl port-forward svc/mycedrive-operator 8080:80
```

Open `http://localhost:8080/dashboard/` — shows registered pods, in-flight migrations with phase, and a trigger-migration form.

## Building from Source

```sh
make build          # builds go-agent and the operator binaries
make test           # unit tests (no cluster required)
make build-agent    # docker image mycedrive/go-agent
make build-dmtcp    # docker image mycedrive/dmtcp (sidecar)
docker build -t mycedrive/operator:dev operator/
```
## Repository Layout

```
operator/         Kubernetes operator (CRDs, reconcilers, REST API, dashboard)
go-agent/         Execution Agent that runs inside application containers
dmtcp/            DMTCP sidecar image and raw manifests
deployment/       Helm chart for the operator
scripts/          make-migratable.sh — StatefulSet onboarding
docs/             User documentation
examples/         Reference application images (Mosquitto, RabbitMQ)
tests/functional/ Agent ↔ operator wire-contract tests
```

## Contributing & License

Contributions are welcome — see [CONTRIBUTING.md](CONTRIBUTING.md). Licensed
under [Apache-2.0](LICENSE).
