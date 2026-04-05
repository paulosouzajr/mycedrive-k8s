# MyceDrive Quick Reference

## API Endpoints

All endpoints run on the Migration Coordinator service (port 80 inside cluster, expose via port-forward).

### Health & Readiness

```bash
# Health check - service is running
curl http://localhost:8080/healthz
# Response: {"status":"ok"}

# Readiness check - service is ready for requests
curl http://localhost:8080/ready
# Response: {"status":"ready"}
```

### Pod Registration

```bash
# Register a pod with the Migration Coordinator
# Execution Agent calls this when starting
curl -X POST http://localhost:8080/register \
  -H "Content-Type: application/json" \
  -d '{
    "pod": "pod-name",
    "namespace": "mig-ready",
    "container_port": 5000
  }'
```

### Pod Removal

```bash
# Unregister a pod from the Migration Coordinator
# Called when pod terminates
curl -X POST http://localhost:8080/remove \
  -H "Content-Type: application/json" \
  -d '{
    "pod": "pod-name",
    "namespace": "mig-ready"
  }'
```

### Copy Checkpoint

```bash
# Initiate checkpoint transfer from source to destination
curl -X POST http://localhost:8080/copy \
  -H "Content-Type: application/json" \
  -d '{
    "pod": "pod-name",
    "namespace": "mig-ready",
    "dest_node": "target-node-name",
    "checkpoint_path": "/path/to/checkpoint"
  }'
```

### Trigger Migration

```bash
# Start a stateful migration of a running pod
curl -X POST http://localhost:8080/migrate \
  -H "Content-Type: application/json" \
  -d '{
    "pod": "pod-name",
    "namespace": "mig-ready",
    "target_node": "destination-node"
  }'
```

---

## Useful kubectl Commands

### Viewing Deployments

```bash
# List all pods in mig-ready namespace
kubectl get pods -n mig-ready

# Show pods on specific nodes
kubectl get pods -n mig-ready -o wide

# Get Migration Coordinator pod name
kubectl get pod -n mig-ready -l app=mycedrive -o jsonpath='{.items[0].metadata.name}'

# Get all nodes
kubectl get nodes -o wide

# Get nodes with labels
kubectl get nodes --show-labels
```

### Pod Details

```bash
# Describe a pod (status, events, containers)
kubectl describe pod <pod-name> -n mig-ready

# Get pod resources (CPU, memory)
kubectl top pod <pod-name> -n mig-ready

# Get pod's assigned node
kubectl get pod <pod-name> -n mig-ready -o jsonpath='{.spec.nodeName}'

# Get pod IP address
kubectl get pod <pod-name> -n mig-ready -o jsonpath='{.status.podIP}'
```

### Logs and Events

```bash
# Stream Migration Coordinator logs
kubectl logs -n mig-ready -l app=mycedrive -f

# View logs from specific container (if pod has multiple)
kubectl logs -n mig-ready <pod-name> -c <container-name>

# Show pod events (useful for debugging)
kubectl get events -n mig-ready --field-selector involvedObject.name=<pod-name>

# Show all cluster events sorted by time
kubectl get events -n mig-ready --sort-by='.lastTimestamp'

# View DMTCP DaemonSet logs
kubectl logs -n mig-ready -l app=dmtcp-daemonset -f
```

### Port Forwarding

```bash
# Forward local port 8080 to Migration Coordinator service:80
kubectl port-forward -n mig-ready svc/mycedrive 8080:80 &

# Forward local port to a specific pod
kubectl port-forward -n mig-ready <pod-name> 8080:80 &

# Kill background port-forward
jobs               # list background jobs
kill %1            # kill job 1
```

### Debugging

```bash
# SSH into a pod (must have bash/sh)
kubectl exec -it -n mig-ready <pod-name> -- /bin/bash

# Run a command in a pod
kubectl exec -n mig-ready <pod-name> -- <command>

# Copy files from pod to local
kubectl cp -n mig-ready <pod-name>:<path> <local-path>

# Copy files from local to pod
kubectl cp <local-path> -n mig-ready <pod-name>:<path>

# Check persistent volumes and claims
kubectl get pvc -n mig-ready
kubectl get pv
```

### RBAC and Permissions

```bash
# Verify ClusterRole exists
kubectl get clusterrole mycedrive
kubectl describe clusterrole mycedrive

# Verify ClusterRoleBinding
kubectl get clusterrolebinding mycedrive
kubectl describe clusterrolebinding mycedrive

# Check service account
kubectl get serviceaccount -n mig-ready
kubectl describe serviceaccount -n mig-ready mycedrive
```

### DaemonSet Management

```bash
# Check DaemonSet status
kubectl get daemonset -n mig-ready

# Describe DaemonSet
kubectl describe daemonset dmtcp-daemonset -n mig-ready

# View DaemonSet pods per node
kubectl get pods -n mig-ready -l app=dmtcp-daemonset -o wide

# Scale or update DaemonSet
kubectl edit daemonset dmtcp-daemonset -n mig-ready
```

---

## Deployment Variables

### Makefile Variables

```bash
# Customize namespace and image tag
make deploy NAMESPACE=production TAG=v1.0.0

# Available variables:
# NAMESPACE     - Kubernetes namespace (default: mig-ready)
# TAG           - Docker image tag (default: dev)
# DOCKERHUB_ORG - Docker Hub organization (default: mycedrive)

# View all make targets
make help
```

---

## Quick Workflows

### 1. Deploy and Test Locally (Minikube)

```bash
# Terminal 1: Start and build
minikube start --cpus=4 --memory=8192
make minikube-build
make minikube-setup

# Terminal 2: Deploy in background
make deploy

# Terminal 3: Watch pods come up
kubectl get pods -n mig-ready -w

# Terminal 4: Port-forward and test
kubectl port-forward -n mig-ready svc/mycedrive 8080:80
curl http://localhost:8080/healthz
```

### 2. Monitor Deployment Progress

```bash
# Watch pods reaching "Running" state
kubectl get pods -n mig-ready -w

# Check logs in real-time
kubectl logs -n mig-ready -l app=mycedrive -f &
kubectl logs -n mig-ready -l app=dmtcp-daemonset -f &

# Watch events
kubectl get events -n mig-ready --watch
```

### 3. Trigger a Test Migration

```bash
# Get pod details
POD=$(kubectl get pods -n mig-ready -o jsonpath='{.items[0].metadata.name}')
NODE=$(kubectl get pod $POD -n mig-ready -o jsonpath='{.status.hostIP}')

# Port forward (if not already)
kubectl port-forward -n mig-ready svc/mycedrive 8080:80 &

# Trigger migration to different node
curl -X POST http://localhost:8080/migrate \
  -H "Content-Type: application/json" \
  -d "{\"pod\":\"$POD\",\"namespace\":\"mig-ready\",\"target_node\":\"node2\"}"

# Monitor migration
kubectl get pods -n mig-ready -w
```

### 4. Clean Debug Session

```bash
# Remove deployment
make undeploy

# View what remains
kubectl get all -n mig-ready

# Remove namespace
kubectl delete namespace mig-ready

# Stop Minikube (if needed)
minikube stop
```

---

## Environment Setup (Bash/Zsh)

Save this to `~/.bashrc` or `~/.zshrc` for convenience:

```bash
# MyceDrive convenience functions
alias k='kubectl'
alias km='kubectl -n mig-ready'
alias klogs='kubectl logs -n mig-ready -f'
alias kpods='kubectl get pods -n mig-ready -o wide'

# Port-forward Migration Coordinator
function myce-pf() {
  kubectl port-forward -n mig-ready svc/mycedrive 8080:80
}

# Get pod running on specific node
function pods-on-node() {
  kubectl get pods -n mig-ready -o wide | grep $1
}

# Trigger migration (requires POD and TARGET_NODE env vars)
function migrate() {
  POD=${1:-$(kubectl get pods -n mig-ready -o jsonpath='{.items[0].metadata.name}')}
  TARGET=${2:-node2}
  echo "Migrating $POD to $TARGET"
  curl -X POST http://localhost:8080/migrate \
    -H "Content-Type: application/json" \
    -d "{\"pod\":\"$POD\",\"namespace\":\"mig-ready\",\"target_node\":\"$TARGET\"}"
}
```

Then use:
```bash
kpods                    # Get all pods
km get events            # Get mig-ready namespace events
myce-pf                  # Port-forward coordinator
migrate pod-name node2   # Trigger migration
```

