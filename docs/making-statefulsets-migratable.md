# Making StatefulSets Migratable with MyceDrive

MyceDrive migrates running pods by checkpointing their memory state with DMTCP and transferring that checkpoint to a destination node. Every pod that participates needs two things: an enriched container image and a pod-spec configuration. This document covers both, plus the MigratableWorkload CR that the MyceDrive operator uses to discover and drive migrations.

---

## How it works (brief)

Each migratable pod runs three cooperating components:

| Component | Where | Role |
|-----------|-------|------|
| **Execution Agent (EA)** | Inside the application container | Wraps the app with `dmtcp_launch`, registers with the operator, drives the checkpoint/restore flow |
| **DMTCP sidecar** | Second container in the pod | Runs `dmtcp_coordinator` on port 7779; exports DMTCP binaries via a shared volume |
| **dmtcp-init initContainer** | Runs once at pod start | Copies binaries from the sidecar image into the shared `emptyDir` so the app container can call them without embedding ~200 MB in its own image |

The MyceDrive operator is deployed once per cluster and orchestrates the migration sequence.

---

## Prerequisites

- MyceDrive operator deployed and reachable:
  ```bash
  helm install mycedrive-operator deployment/operator \
    --namespace mig-ready --create-namespace
  ```
  The operator exposes a Service named `mycedrive` in the same namespace on port 80. The EA connects to it via `mycedrive.<namespace>.svc.cluster.local`.
- `kubectl` configured against the target cluster.
- Docker (to rebuild your application image).
- The target StatefulSet already exists in the cluster.

---

## Step 1 — Modify the application image

Your application image must contain the `go-agent` binary and a symlink named `end_container` pointing to it. The binary is statically compiled (`CGO_ENABLED=0`) so it runs on any Linux container regardless of the base image.

Add these two lines to your Dockerfile (after your application layers):

```dockerfile
# Inject the MyceDrive Execution Agent
COPY --from=docker.io/mycedrive/go-agent:dev /build/go-agent /usr/local/bin/go-agent
RUN ln -s /usr/local/bin/go-agent /usr/local/bin/end_container
```

The `go-agent` binary path inside `mycedrive/go-agent:dev` is `/build/go-agent` (produced by `make build-agent`). Adjust the path if you build the image yourself.

No changes to your `CMD` or `ENTRYPOINT` are required. The EA is invoked at container start via the `START_UP` env var — it calls `dmtcp_launch` internally to wrap that command.

Rebuild and push your image before running the script.

---

## Step 2 — Run make-migratable.sh

The script patches the StatefulSet in-place and creates the MigratableWorkload CR. It is idempotent: running it twice produces the same result.

```bash
./scripts/make-migratable.sh \
  -n <namespace> \
  -s <statefulset-name> \
  -u "<application startup command>" \
  [-c <container-name>]              # default: first container
  [-m <operator-service-host>]       # default: mycedrive.mig-ready.svc.cluster.local
  [-i <dmtcp-image>]                 # default: mycedrive/dmtcp:dev
  [-d <checkpoint-dir>]              # default: /dmtcp/checkpoints
  [--process-migration true|false]   # default: true
  [--volume-migration true|false]    # default: true
  [--pre-sync-rounds N]              # default: 1 (N >= 0)
  [--no-cr]                          # skip MigratableWorkload CR creation
  [--dry-run]                        # print all YAML; do not apply anything
```

### Example — mosquitto StatefulSet

```bash
./scripts/make-migratable.sh \
  -n mig-ready \
  -s mosquitto \
  -c mosquitto \
  -u "/usr/sbin/mosquitto -c /etc/mosquitto/mosquitto.conf"
```

### Example — disable volume migration, increase pre-sync rounds

```bash
./scripts/make-migratable.sh \
  -n mig-ready \
  -s mosquitto \
  -c mosquitto \
  -u "/usr/sbin/mosquitto -c /etc/mosquitto/mosquitto.conf" \
  --volume-migration false \
  --pre-sync-rounds 3
```

### Dry-run first

Always preview the patches before applying:

```bash
./scripts/make-migratable.sh \
  -n mig-ready -s mosquitto -c mosquitto \
  -u "/usr/sbin/mosquitto -c /etc/mosquitto/mosquitto.conf" \
  --dry-run
```

### What the script adds

| Addition | Detail |
|----------|--------|
| `dmtcp-shared` emptyDir volume | Shared between all containers in the pod |
| `dmtcp-init` initContainer | Copies DMTCP binaries to `/dmtcp` from the sidecar image |
| `dmtcp` sidecar container | Runs `dmtcp_coordinator` on port 7779; mounts the shared volume at `/share` |
| Env vars on the app container | `MIGR_COOR`, `POD_NAME`, `POD_IP`, `DMTCP_COORD_HOST`, `DMTCP_CHECKPOINT_DIR`, `START_UP` |
| Toggle env vars (when non-default) | `ENABLE_PROCESS_MIGRATION=false` and/or `ENABLE_VOLUME_MIGRATION=false` |
| `volumeMount` on the app container | `/dmtcp` — makes DMTCP binaries and checkpoint files accessible |
| `preStop` lifecycle hook | Calls `/dmtcp/bin/end_container <operator-host> $(POD_NAME) <ckpt-dir>` |
| Pod template label | `mig-ready: "true"` — matches the operator's default placement label |
| ClusterRoleBinding | Binds the pod's ServiceAccount to `mycedrive-coordinator-role` |
| MigratableWorkload CR | Tells the operator this StatefulSet is migration-eligible (see below) |

---

## Step 3 — MigratableWorkload CR

The script creates a `MigratableWorkload` CR (group `mycedrive.io/v1alpha1`, shortname `mw`) in the same namespace as the workload. The operator uses this CR to discover migratable workloads and read per-workload migration parameters.

The operator's legacy `/migrate` endpoint will auto-create a missing CR if you trigger a migration without running the script first, so the CR is **recommended but not strictly required**. Use `--no-cr` if you manage CRs separately.

A minimal CR (all-defaults) looks like:

```yaml
apiVersion: mycedrive.io/v1alpha1
kind: MigratableWorkload
metadata:
  name: mosquitto
  namespace: mig-ready
spec:
  workloadRef:
    kind: StatefulSet
    name: mosquitto
  placementLabel:
    key: mig-ready
    value: "true"
```

Non-default flags add fields. For example, `--volume-migration false --pre-sync-rounds 3` produces:

```yaml
spec:
  workloadRef:
    kind: StatefulSet
    name: mosquitto
  placementLabel:
    key: mig-ready
    value: "true"
  volumeMigration: false
  preSyncRounds: 3
```

Fields at their defaults are omitted to keep the CR minimal.

### CR spec reference

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `workloadRef.kind` | `StatefulSet\|Deployment` | (required) | Controller type |
| `workloadRef.name` | string | (required) | Controller name |
| `placementLabel.key` | string | `mig-ready` | Node/pod selector key the operator uses |
| `placementLabel.value` | string | `"true"` | Node/pod selector value |
| `checkpointDir` | string | `/dmtcp/checkpoints` | Path inside the pod |
| `transferPort` | int | `2486` | TCP port for checkpoint file transfer |
| `layerCount` | int | `1` | Number of overlayfs layers to checkpoint |
| `processMigration` | bool | `true` | Enable DMTCP process checkpointing |
| `volumeMigration` | bool | `true` | Enable overlayfs volume checkpointing |
| `preSyncRounds` | int ≥ 0 | `1` | Pre-migration dirty-page sync iterations |

---

## Step 4 — Verify the pod is wrapped

After the rollout completes:

```bash
# Wait for the rollout
kubectl rollout status sts/<statefulset-name> -n <namespace>

# Confirm DMTCP binaries are present in the app container
kubectl exec -it <statefulset-name>-0 -n <namespace> -c <container-name> \
  -- ls /dmtcp/bin/
# Expected: dmtcp_launch  dmtcp_coordinator  dmtcp_command  dmtcp_restart  end_container

# Confirm the EA registered with the operator
kubectl logs <statefulset-name>-0 -n <namespace> -c <container-name> \
  | grep -i "register"
# Expected: "Registering container with MC at ..." and "Register response from MC: ..."

# Check the MigratableWorkload CR
kubectl get mw <statefulset-name> -n <namespace>
```

---

## Triggering a migration

Once the pod is registered, use the operator API (same Service, port 80):

```bash
OP=$(kubectl get svc mycedrive -n mig-ready -o jsonpath='{.spec.clusterIP}')

curl -s -X POST "http://${OP}/migrate" \
  -H "Content-Type: application/json" \
  -d '{
    "deployment": "<statefulset-name>",
    "originNode": "<source-node>",
    "destNode":   "<destination-node>"
  }'
```

---

## Known limitations and open items

### PVC handling (critical for StatefulSets)

StatefulSets commonly use `volumeClaimTemplates` to provision per-pod PVCs. MyceDrive checkpoints **process memory and open file descriptors** via DMTCP; it does **not** checkpoint or transfer PVC data. After migration:

- Files that were open at checkpoint time are restored in memory by DMTCP.
- Changes to the PVC written after the checkpoint was taken are not replicated to the destination node.

Operators must ensure one of the following before triggering migration:

1. The PVC is backed by a distributed or replicated storage class (e.g. Longhorn, Ceph, NFS) accessible from both nodes, and the PVC is `ReadWriteMany` or the source pod is stopped before the destination mounts it.
2. The application's persistent data is fully described by in-memory state (unusual but possible for stateless-ish apps).
3. The StatefulSet uses `ReadWriteOnce` PVCs and the migration procedure includes a manual data-sync step before restarting the destination pod.

The overlayfs layer stack (`go-agent/overlay/`) checkpoints the container's writable overlay layer — it does not cover external PVC mounts.

### StatefulSet controller semantics

The operator's `/migrate` endpoint was originally built around Deployments (scale-up/scale-down). For StatefulSets:

- Scaling up creates a pod with a new ordinal, not a replacement at the same stable identity.
- The `preStop` hook and checkpoint flow work correctly regardless of controller type.
- You may need to manually delete the source pod after the destination pod has registered and received the checkpoint, rather than relying on the operator's automated scale-down sequence.

Full StatefulSet support (sticky identity migration) is tracked as an open item in the operator.

### DMTCP process compatibility

- Multi-threaded processes generally work but are more complex.
- Processes using `fork` or `exec` after checkpoint may not restore cleanly.
- JVM-based workloads (Kafka, Elasticsearch) require DMTCP plugin support and are untested.
- Processes that bind to ports before `dmtcp_launch` wraps them will not have those connections preserved.

### Network connection preservation

DMTCP preserves TCP connections established **after** `dmtcp_launch` wrapped the process. Use a Kubernetes Service with a stable ClusterIP in front of the StatefulSet so clients reconnect transparently after migration.

### Image build requirement

The script patches the pod spec but cannot modify your container image. The `go-agent` binary and `end_container` symlink must be present in the image before the script is run. If they are missing the pod starts but the preStop hook fails silently and migration does not function.

---

## Reference: environment variables consumed by the EA

| Variable | Required | Description |
|----------|----------|-------------|
| `MIGR_COOR` | Yes | Hostname (no scheme) of the operator service |
| `POD_NAME` | Yes | Injected via downward API; used as the pod identifier |
| `POD_IP` | Yes | Injected via downward API; used as the checkpoint transfer endpoint |
| `START_UP` | Yes | Full startup command to run under `dmtcp_launch` |
| `DMTCP_COORD_HOST` | Yes | Hostname of the DMTCP coordinator (`127.0.0.1` in sidecar mode) |
| `DMTCP_CHECKPOINT_DIR` | Yes | Directory where DMTCP writes checkpoint files |
| `CONTAINER_PORT` | No | Override the EA's file-transfer TCP port (default: 2486) |
| `ENABLE_PROCESS_MIGRATION` | No | Set to `false` to disable DMTCP process checkpointing (default: `true`) |
| `ENABLE_VOLUME_MIGRATION` | No | Set to `false` to disable overlayfs volume checkpointing (default: `true`) |
