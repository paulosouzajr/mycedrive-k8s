# Making StatefulSets Migratable with MyceDrive

MyceDrive migrates running pods by checkpointing their memory state with DMTCP and transferring that checkpoint to a destination node. Every pod that participates needs two things: an enriched container image and a pod-spec configuration. This document covers both.

---

## How it works (brief)

Each migratable pod runs three cooperating components:

| Component | Where | Role |
|-----------|-------|------|
| **Execution Agent (EA)** | Inside the application container | Wraps the app with `dmtcp_launch`, registers with the Migration Coordinator, drives the checkpoint/restore flow |
| **DMTCP sidecar** | Second container in the pod | Runs `dmtcp_coordinator` on port 7779; exports DMTCP binaries via a shared volume |
| **dmtcp-init initContainer** | Runs once at pod start | Copies binaries from the sidecar image into the shared `emptyDir` so the app container can call them without embedding ~200 MB in its own image |

The Migration Coordinator (`go-server`) is deployed once per cluster and orchestrates the migration sequence.

---

## Prerequisites

- MyceDrive Migration Coordinator deployed and reachable (see [deployment README](../README.md#deploying-to-a-cluster)).
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

The `go-agent` binary path inside `mycedrive/go-agent:dev` is `/build/go-agent` (produced by `make build-agent`). If you build the image yourself adjust the path accordingly.

No changes to your `CMD` or `ENTRYPOINT` are required. The EA starts as the pod's main process (invoked via `START_UP` env var) — it calls `dmtcp_launch` internally to wrap whatever command `START_UP` points to.

Rebuild and push your image before running the script.

---

## Step 2 — Run make-migratable.sh

The script patches the StatefulSet in-place. It is idempotent: running it twice produces the same result.

```bash
./scripts/make-migratable.sh \
  -n <namespace> \
  -s <statefulset-name> \
  -u "<application startup command>" \
  [-c <container-name>]          # default: first container
  [-m <migration-coordinator-host>]  # default: mycedrive.mig-ready.svc.cluster.local
  [-i <dmtcp-image>]             # default: mycedrive/dmtcp:dev
  [-d <checkpoint-dir>]          # default: /dmtcp/checkpoints
  [--dry-run]                    # print YAML only; do not apply
```

### Example — mosquitto StatefulSet

```bash
./scripts/make-migratable.sh \
  -n mig-ready \
  -s mosquitto \
  -c mosquitto \
  -u "/usr/sbin/mosquitto -c /etc/mosquitto/mosquitto.conf"
```

### Dry-run first

Always preview the patch before applying:

```bash
./scripts/make-migratable.sh \
  -n mig-ready -s mosquitto \
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
| `volumeMount` on the app container | `/dmtcp` — makes DMTCP binaries and checkpoint files accessible |
| `preStop` lifecycle hook | Calls `/dmtcp/bin/end_container <mc-host> $(POD_NAME) <ckpt-dir>` |
| Pod template label | `mig-ready: "true"` — marks the pod as migration-eligible |
| ClusterRoleBinding | Binds the pod's ServiceAccount to `mycedrive-coordinator-role` |

---

## Step 3 — Verify the pod is wrapped

After the rollout completes:

```bash
# Wait for the rollout
kubectl rollout status sts/<statefulset-name> -n <namespace>

# Confirm the DMTCP binaries are present in the app container
kubectl exec -it <statefulset-name>-0 -n <namespace> -c <container-name> \
  -- ls /dmtcp/bin/

# Expected output includes: dmtcp_launch  dmtcp_coordinator  dmtcp_command  dmtcp_restart  end_container

# Confirm the EA registered with the Migration Coordinator
kubectl logs <statefulset-name>-0 -n <namespace> -c <container-name> \
  | grep -i "register"

# Expected: "Registering container with MC at ..." and "Register response from MC: ..."
```

---

## Triggering a migration

Once the pod is registered, use the Migration Coordinator API:

```bash
MC=$(kubectl get svc mycedrive -n mig-ready -o jsonpath='{.spec.clusterIP}')

curl -s -X POST "http://${MC}/migrate" \
  -H "Content-Type: application/json" \
  -d '{
    "deployment": "<statefulset-name>",
    "originNode": "<source-node>",
    "destNode":   "<destination-node>"
  }'
```

Note: the `/migrate` endpoint currently takes a `deployment` field; StatefulSets are supported as long as the pod replica count and node selectors allow the controller to schedule on the destination node.

---

## Known limitations and open items

### PVC handling (critical for StatefulSets)

StatefulSets commonly use `volumeClaimTemplates` to provision per-pod PVCs. MyceDrive checkpoints **process memory and open file descriptors** via DMTCP; it does **not** checkpoint or transfer PVC data. After migration:

- Files that were open at checkpoint time are restored in memory by DMTCP.
- Changes to the PVC written after the checkpoint was taken are not replicated to the destination node.

Operators must ensure one of the following before triggering migration:

1. The PVC is backed by a distributed or replicated storage class (e.g. Longhorn, Ceph, NFS) accessible from both nodes, **and** the PVC is `ReadWriteMany` or the source pod is stopped before the destination mounts it.
2. The application's persistent data is fully described by in-memory state (i.e. the PVC content can be reconstructed from the DMTCP checkpoint alone — unusual but possible for stateless-ish apps).
3. The StatefulSet uses `ReadWriteOnce` PVCs and the migration procedure includes a manual data-sync step before restarting the destination pod.

The overlayfs layer stack managed by the EA (`go-agent/overlay/`) checkpoints the container's overlay layers, which covers the container's writable layer but not external PVC mounts.

### StatefulSet vs Deployment

The `/migrate` endpoint is designed around `Deployments` (it scales up/down replica counts). For StatefulSets the sequence is different:

- StatefulSets maintain stable pod identities (`<name>-0`, `<name>-1`); scaling up creates a pod with a new ordinal, not a replacement at the same identity.
- The `preStop` hook and checkpoint flow work correctly regardless of controller type.
- You may need to manually delete the source pod after the destination pod has registered and received the checkpoint, rather than relying on the MC's scale-up/scale-down sequence.

Proper StatefulSet support (sticky identity migration) is not yet implemented in `go-server/api/manager.go`.

### DMTCP process compatibility

DMTCP does not checkpoint all process types reliably:

- Multi-threaded processes generally work but are more complex.
- Processes using `fork` or `exec` after checkpoint may not restore cleanly.
- JVM-based workloads (Kafka, Elasticsearch) require DMTCP plugin support and are untested.
- Processes that bind to ports before `dmtcp_launch` wraps them will not have those connections preserved.

### Network connection preservation

DMTCP preserves TCP connections that were established **after** `dmtcp_launch` wrapped the process. Connections that exist before the first `dmtcp_launch` call (e.g. established during the Kubernetes readiness probe window) are not preserved. Use a Kubernetes Service with a stable ClusterIP in front of the StatefulSet so clients reconnect transparently.

### Image build requirement

The `make-migratable.sh` script patches the **pod spec** but cannot modify your container image. The `go-agent` binary and `end_container` symlink must be present in the application image before the script is run. If the image is missing these binaries the pod will start but the preStop hook will fail silently and migration will not function.

---

## Reference: environment variables consumed by the EA

| Variable | Required | Description |
|----------|----------|-------------|
| `MIGR_COOR` | Yes | Hostname (no scheme) of the Migration Coordinator service |
| `POD_NAME` | Yes | Injected via downward API; used as the pod identifier with the MC |
| `POD_IP` | Yes | Injected via downward API; used as the transfer endpoint address |
| `START_UP` | Yes | Full startup command to run under `dmtcp_launch` |
| `DMTCP_COORD_HOST` | Yes | Hostname of the DMTCP coordinator (always `127.0.0.1` for sidecar mode) |
| `DMTCP_CHECKPOINT_DIR` | Yes | Directory where DMTCP writes checkpoint files |
| `CONTAINER_PORT` | No | Override the EA's file-transfer TCP port (default: 2486) |
