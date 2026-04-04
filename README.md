# MyceDrive

MyceDrive is a stateful pod migration system for Kubernetes. It checkpoints a running container's memory state using DMTCP, transfers the checkpoint to a destination node, and restarts the container from the saved state — with open network connections preserved end-to-end.

## Architecture

The system has two components.

**Execution Agent (EA)** — runs inside every application container. It wraps the container entry point with `dmtcp_launch`, controls the DMTCP coordinator lifecycle, manages the overlayfs layer stack, and executes the preStop checkpoint flow via the `end_container` hook.

**Migration Coordinator (MC)** — a REST API (Go + Gin) deployed once per cluster, typically alongside the Kubernetes control plane. It tracks registered containers, orchestrates the migration sequence by calling the Kubernetes API, and signals the destination EA when a checkpoint is ready to be loaded.

Each pod that participates in migration contains two containers:

- The application container with the EA binary injected as an image layer.
- A lightweight DMTCP sidecar (`dmtcp:dev`) running `dmtcp_coordinator` and exporting a shared volume with the DMTCP binaries and checkpoint files.

## Repository Layout

```
go-agent/           Execution Agent — runs inside the container
  main.go           Entry point; dispatches to end_container or agent loop
  dmtcp/            DMTCP coordinator lifecycle (Launch/Checkpoint/Restart)
  overlay/          Overlayfs layer management
  utils/            Run(), EndContainer(), SendFile(), postJSON()

go-server/          Migration Coordinator REST API
  main.go           Gin router setup
  api/manager.go    HTTP handlers: /register /remove /copy /migrate
  kub/kub.go        Kubernetes API wrappers (pods, deployments, nodes)
  logs/logs.go      Structured logger

dmtcp/              Deployment manifests
  Dockerfile        DMTCP sidecar image (Ubuntu 22.04, builds from source)
  dmtcp-daemonset.yml   DaemonSet: one coordinator + EA per node
  deploy_sidecar.yml    Pod spec showing sidecar + lifecycle hooks
  deploy_full_stack.yml Full stack: Namespace, RBAC, MC, app Deployment

deployment/         Helm chart for the Migration Coordinator
  myce-server/mycedrive/

examples/           Reference application images (Mosquitto, RabbitMQ)
Makefile            Build, push, deploy, and test targets
go.work             Go workspace linking go-agent and go-server modules
```

## Prerequisites

- Go 1.18 or later
- Docker
- A running Kubernetes cluster (Minikube works for local testing)
- `kubectl` configured and pointing at the target cluster
- Docker Hub access to the [mycedrive](https://hub.docker.com/u/mycedrive) organisation (pre-built images are public)

## Building

Build both binaries from the repository root:

```sh
make build
```

This compiles `go-agent` (with `CGO_ENABLED=0` for a static binary) and `go-server`, placing the outputs in their respective `build/` directories.

To build individual Docker images:

```sh
make build-agent    # builds mycedrive/go-agent
make build-server   # builds mycedrive/go-server (Migration Coordinator)
make build-dmtcp    # builds mycedrive/dmtcp
```

To build and push all three images to Docker Hub:

```sh
make login   # prompts for credentials (DOCKER_USERNAME must be an org member)
make build push
```

Images are published to `docker.io/mycedrive/` and are also built automatically
by GitHub Actions on every push to `main` that touches the relevant source directory.

Pre-built public images:
- `docker.io/mycedrive/go-server`
- `docker.io/mycedrive/go-agent`
- `docker.io/mycedrive/dmtcp`

## Running Tests

```sh
make test
```

This runs the unit test suites for `go-agent/overlay` and `go-agent/dmtcp`. All tests are pure Go with no external dependencies and no Kubernetes cluster required.

## Deploying to a Cluster

### Full stack (raw manifests)

```sh
kubectl apply -f dmtcp/deploy_full_stack.yml
```

This creates the `mig-ready` namespace, an RBAC ClusterRole and binding, a Deployment and Service for the Migration Coordinator, and a sample application Deployment with the EA sidecar enabled.

### Helm (Migration Coordinator only)

The chart is published to GitHub Pages and can be installed directly from the public Helm repository:

```sh
helm repo add mycedrive https://paulosouzajr.github.io/mycedrive-k8s
helm repo update
helm install mycedrive mycedrive/mycedrive
```

To override the image tag:

```sh
helm install mycedrive mycedrive/mycedrive --set image.tag=dev
```

To install from the local chart (development):

```sh
helm install mycedrive deployment/myce-server/mycedrive \
  --set image.repository=mycedrive/go-server \
  --set image.tag=dev
```

### DaemonSet mode

```sh
kubectl apply -f dmtcp/dmtcp-daemonset.yml
```

Deploys the DMTCP coordinator and EA as a DaemonSet so every worker node runs a coordinator instance.

## Making an Application Container Migration-Ready

Add the EA binary as an extra image layer and configure the pod lifecycle hooks:

```dockerfile
# In your application Dockerfile
COPY --from=go-agent:latest /usr/local/bin/go-agent /usr/local/bin/go-agent
RUN ln -s /usr/local/bin/go-agent /usr/local/bin/end_container
```

In the pod spec:

```yaml
containers:
  - name: app
    image: your-app:latest
    env:
      - name: MIGR_COOR
        value: "http://migration-coordinator.mig-ready.svc.cluster.local:8080"
      - name: START_UP
        value: "/usr/sbin/nginx -g 'daemon off;'"
    lifecycle:
      preStop:
        exec:
          command: ["/usr/local/bin/end_container"]
    volumeMounts:
      - name: dmtcp-shared
        mountPath: /dmtcp

  - name: dmtcp
    image: dmtcp:dev
    env:
      - name: DMTCP_CHECKPOINT_DIR
        value: /dmtcp/checkpoints
    volumeMounts:
      - name: dmtcp-shared
        mountPath: /share

volumes:
  - name: dmtcp-shared
    emptyDir: {}
```

See `dmtcp/deploy_sidecar.yml` for a complete example.

## Migration Coordinator API

All endpoints accept and return JSON.

### POST /register

Called by the EA when a container starts. Returns whether this start is a normal launch or a migration resume.

Request:
```json
{ "podName": "nginx-7f9d8c", "podAddress": "10.0.0.5", "isNew": true, "isMig": false }
```

Response (first registration — normal start):
```json
{ "podName": "nginx-7f9d8c", "podAddress": "10.0.0.5", "isNew": true, "isMig": false }
```

Response (pod already registered — migration in progress):
```json
{ "podName": "nginx-7f9d8c", "podAddress": "10.0.0.5", "isNew": false, "isMig": true }
```

### POST /remove

Called by the EA from the `preStop` hook. Returns whether the EA must produce a checkpoint before the container stops.

Request:
```json
{ "podName": "nginx-7f9d8c" }
```

Response:
```json
{ "needsCheckpoint": true }
```

### POST /copy

Called by the source EA after checkpoint files have been written. Marks the pod so the destination EA learns the checkpoint is ready.

Request:
```json
{ "podName": "nginx-7f9d8c", "checkpointDir": "/dmtcp/checkpoints" }
```

Response:
```json
{ "status": "copy_initiated", "pod": "nginx-7f9d8c" }
```

### POST /migrate

Initiates a pod migration. The MC labels the source and destination nodes, scales up the deployment so Kubernetes places the new replica on the destination, and deletes the source pod to trigger the preStop checkpoint flow.

Request:
```json
{
  "deployment": "nginx",
  "originNode": "worker-01",
  "destNode":   "worker-02",
  "label":      "mig-ready:true"
}
```

`label` defaults to `"mig-ready:true"` if omitted. It must follow the `key:value` format and must match the node selector configured in the application Deployment.

Response:
```json
{ "status": "migration_started", "deployment": "nginx" }
```

## Migration Sequence

1. A user or operator calls `POST /migrate` with the deployment name, source node, and destination node.
2. The MC marks the deployment as migrating and updates node labels so Kubernetes constrains pod placement.
3. The MC scales up the deployment; Kubernetes creates the new pod on the destination node.
4. The destination EA calls `/register`. Because the pod name is already known, the MC responds with `isMig: true`, and the EA blocks waiting for the checkpoint.
5. The MC scales back down and deletes the source pod, triggering the `preStop` hook.
6. The source EA calls `/remove`, receives `needsCheckpoint: true`, and instructs the DMTCP coordinator to produce a checkpoint.
7. The source EA calls `/copy` to notify the MC, then streams the checkpoint files to the destination node via a direct TCP connection.
8. The MC's pending `/register` response unblocks, returning the checkpoint path to the destination EA.
9. The destination EA instructs the DMTCP coordinator to restart the process from the checkpoint.

## Development

Format and vet all code:

```sh
make fmt
make vet
```

Build without Docker (requires Go toolchain):

```sh
cd go-agent  && go build ./...
cd go-server && go build ./...
```


## To do

- [x] Create socket communication between containers
- [x] Create mock service to transfer checkpoint files
- [x] Write unit tests for communication
- [ ] Write unit tests for overlay consistency
- [ ] Write unit tests for dmtcp consistency
- [ ] Replace Sockets in server with RESTful API with Go and Gin
- [ ] DMTCP Daemonset

***

## Project status

??
