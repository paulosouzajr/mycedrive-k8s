# Migration Test Scenarios

Three ready-to-run scenarios that exercise MyceDrive's migration mechanisms —
together and individually — using the same application (Mosquitto MQTT broker)
and the same observable state: a **retained MQTT message**. Where that message
survives from tells you which mechanism did the work.

| Scenario | Dir | App | Process (DMTCP) | Volume (overlay) | Where the state lives |
|----------|-----|-----|:---:|:---:|------------------------|
| Both | `both/` | `mq-both` | ✅ | ✅ | Broker memory **and** `mosquitto.db` |
| Process only | `process-only/` | `mq-proc` | ✅ | ❌ | Broker memory only (persistence off) |
| Volume only | `volume-only/` | `mq-vol` | ❌ | ✅ | `mosquitto.db` only (process restarts fresh) |

- **process-only** proves DMTCP works alone: persistence is disabled, so the
  retained message exists *only* in process memory. If it survives, the
  process was checkpointed and restored.
- **volume-only** proves the overlay mechanism works alone: the process is not
  checkpointed, so the fresh broker on the target node can only know the
  message by reloading the migrated `mosquitto.db`.
- **both** is the full MyceDrive experience: memory, open sockets and volume
  move together.

## Prerequisites

1. A cluster with **at least 2 schedulable Linux nodes** (for a local trial:
   `minikube start --nodes 2` or a 2-node kind cluster).
2. The **operator installed** (see the main [README](../../README.md)). The
   manifests assume the default legacy-alias Service
   `mycedrive.mig-ready.svc.cluster.local`; edit `MIGR_COOR` in the YAML if
   your operator lives elsewhere.
3. **Images available on the nodes** (build order matters — the dmtcp image
   bundles the agent):

   ```sh
   make build-agent build-dmtcp        # mycedrive/go-agent:dev, mycedrive/dmtcp:dev
   make build-example                  # mycedrive/mosquitto:dev
   # minikube: minikube image load mycedrive/dmtcp:dev mycedrive/mosquitto:dev
   ```

4. The `both` and `volume-only` scenarios run the app container **privileged**
   (the overlay mount syscall needs CAP_SYS_ADMIN). `process-only` does not.

## Quick start — the script does everything

```sh
./examples/run-scenario.sh both              # or: process-only | volume-only
```

The script: checks prerequisites → deploys the scenario → waits for the pod →
publishes a retained message → creates a `Migration` CR targeting another
node → prints each phase transition (`Pending → Syncing → Checkpointing →
Transferring → Restoring → Completed`) → waits for the pod on the target
node → subscribes and compares the retained message → **PASS/FAIL**.

Useful flags: `-n NAMESPACE` (default `mig-ready`), `-t TARGET_NODE` (default:
auto-picked), `--skip-deploy` (re-run a migration on an existing deployment),
`--cleanup` (tear the scenario down).

## Manual walkthrough (what the script automates)

Using `both` as the example; substitute `mq-proc`/`mq-vol` for the others.

```sh
# 1. Deploy
kubectl apply -n mig-ready -f examples/scenarios/both/
kubectl rollout status sts/mq-both -n mig-ready

# 2. Seed observable state (retained message)
kubectl exec -n mig-ready mq-both-0 -c mosquitto -- \
  mosquitto_pub -t demo/state -m "hello-before-migration" -r
sleep 6   # let mosquitto autosave flush the DB (volume scenarios)

# 3. Note the current node, pick another
kubectl get pod mq-both-0 -n mig-ready -o wide

# 4. Trigger the migration
cat <<EOF | kubectl create -n mig-ready -f -
apiVersion: mycedrive.io/v1alpha1
kind: Migration
metadata:
  generateName: mq-both-
spec:
  workloadName: mq-both
  sourceNode: <node-from-step-3>
  targetNode: <another-node>
EOF

# 5. Watch it move
kubectl get mig -n mig-ready -w

# 6. Verify: same retained message, new node
kubectl get pod mq-both-0 -n mig-ready -o wide
kubectl exec -n mig-ready mq-both-0 -c mosquitto -- \
  mosquitto_sub -t demo/state -C 1 -W 10
```

The dashboard shows the same story live:
`kubectl port-forward svc/mycedrive-operator 8080:80` → `http://localhost:8080/dashboard/`.

## What to look at when it doesn't work

- **Operator decisions**: `kubectl logs deploy/mycedrive-operator -n <operator-ns>`
  and `kubectl get mig <name> -o yaml` (the status block carries the failure
  reason and per-phase timestamps; each phase times out after 10 minutes).
- **Source-side checkpoint** (preStop): the terminating pod's `mosquitto`
  container logs — the agent prints the `/remove` response, checkpoint
  creation, and every transferred frame.
- **Target-side restore**: the new pod's logs — the agent prints each received
  layer/checkpoint file, the overlay mount, and the `dmtcp_restart` exec.
- Common causes: images missing on the target node (`ImagePullBackOff` with
  local-only `:dev` tags), forgetting `make build-agent` before
  `make build-dmtcp` (agent missing from `/dmtcp/bin`), non-privileged
  container in a volume scenario (overlay mount fails), publishing state and
  migrating before autosave flushed the DB in `volume-only` (message lost by
  design — that's the mechanism boundary, not a bug).
