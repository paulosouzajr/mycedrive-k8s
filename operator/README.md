# MyceDrive Operator

Kubernetes operator acting as the Migration Coordinator. It reconciles two
CRDs and embeds the REST API the Execution Agents (`go-agent`) talk to.

## CRDs (group `mycedrive.io/v1alpha1`)

- **MigratableWorkload** (`mw`) — a wrapped StatefulSet (primary) or
  Deployment under management. Spec selects the workload, the placement node
  label, the checkpoint dir and the per-workload mechanism toggles
  `processMigration` (DMTCP) / `volumeMigration` (overlayfs layers) plus
  `preSyncRounds`. Status mirrors agent registrations so they survive
  operator restarts.
- **Migration** (`mig`) — one migration request (`workloadName`, optional
  `podName`, `sourceNode`, `targetNode`). Status phases:
  `Pending → [Syncing] → Checkpointing → Transferring → Restoring →
  Completed | Failed`.

## REST API (port 8080)

Legacy agent contract (unchanged shapes): `POST /register`, `POST /remove`,
`POST /copy`, `POST /migrate`. Additive endpoints for the fixed agent:
`POST /sync`, `POST /restored`, `GET /poll?podName=`. UI endpoints:
`GET /pods` (legacy shape), `GET /api/v1/pods`, `GET+POST /api/v1/migrations`,
dashboard at `/dashboard/`.

History & metrics module (optional, on by default): `GET /api/v1/history`
returns the recorded migrations (per-phase steps with durations, downtime
window, workload accessibility) plus aggregate metrics (success rate, average
duration/downtime, per-phase averages). `GET+POST /api/v1/history/config`
reads/toggles collection at runtime (`{"enabled": true|false}`); the
`--history-enabled` / `--history-limit` flags (Helm: `history.enabled`,
`history.limit`) set the start-up state and the in-memory record cap.
History is rebuilt coarsely from Migration CRs after a restart.

## Build

```sh
cd operator
go build ./...                                  # binary
docker build -t mycedrive/operator:dev .        # image
```

## Deploy

```sh
helm install mycedrive-operator deployment/operator \
  --namespace mig-ready --create-namespace
```

The chart installs the CRDs, RBAC, the operator Deployment and a Service
named `mycedrive` so existing `MIGR_COOR` values keep resolving.
