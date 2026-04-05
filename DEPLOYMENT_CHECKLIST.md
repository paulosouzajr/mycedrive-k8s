# MyceDrive Deployment Checklist

Use this checklist to track your deployment progress.

## Prerequisites ✓

- [ ] Go 1.18+ installed (`go version`)
- [ ] Docker installed (`docker --version`)
- [ ] kubectl installed and configured (`kubectl cluster-info`)
- [ ] Helm 3+ installed (`helm version`)
- [ ] Kubernetes cluster ready (Minikube or production)
- [ ] Docker Hub account (for pushing images) or plan to use pre-built images

## Unit Tests (Optional but Recommended)

- [ ] Run `make test` from repository root
- [ ] All go-agent tests pass
- [ ] All go-server tests pass

## For Minikube Deployment

### Build Phase
- [ ] Start minikube: `minikube start --cpus=4 --memory=8192`
- [ ] Run `make minikube-build` to build images
- [ ] Verify images built: `minikube image ls | grep mycedrive`

### Setup Phase
- [ ] Run `make minikube-setup NAMESPACE=mig-ready`
- [ ] Verify namespace created: `kubectl get namespace mig-ready`

### Deploy Phase
- [ ] Run `make deploy NAMESPACE=mig-ready TAG=dev`
- [ ] Wait for Migration Coordinator to be ready
- [ ] Wait for DMTCP DaemonSet to roll out
- [ ] Wait for example app pod to be ready

### Verification Phase
- [ ] All pods running: `kubectl get pods -n mig-ready`
- [ ] Check pod count:
  - [ ] 1 Migration Coordinator pod
  - [ ] 1+ DMTCP DaemonSet pods (one per node)
  - [ ] 1 Example app pod (with 2 containers: app + sidecar)
- [ ] Port-forward to coordinator: `kubectl port-forward -n mig-ready svc/mycedrive 8080:80 &`
- [ ] Health check: `curl http://localhost:8080/healthz`
- [ ] Readiness check: `curl http://localhost:8080/ready`
- [ ] View coordinator logs: `kubectl logs -n mig-ready -l app=mycedrive`

---

## For Production Cluster Deployment

### Build & Push Phase
- [ ] Logged into Docker Hub: `docker login` (or run `make login`)
- [ ] Run `make build` to build images
- [ ] Run `make push` to push images to Docker Hub
- [ ] Verify images on Docker Hub: `docker pull mycedrive/go-server:dev`

### Deploy Phase
- [ ] Run `make deploy NAMESPACE=mig-ready TAG=dev` (or your custom namespace/tag)
- [ ] Helm release installed: `helm list -n mig-ready`
- [ ] DMTCP DaemonSet deployed: `kubectl get daemonset -n mig-ready`

### Verification Phase
- [ ] All pods running: `kubectl get pods -n mig-ready -o wide`
- [ ] Pod distribution checked:
  - [ ] Migration Coordinator on control/worker node
  - [ ] DMTCP DaemonSet on all nodes
  - [ ] Application pod running with sidecar
- [ ] RBAC configured:
  - [ ] ClusterRole exists: `kubectl get clusterrole mycedrive`
  - [ ] ClusterRoleBinding exists: `kubectl get clusterrolebinding mycedrive`
- [ ] Health endpoints responding:
  - [ ] `/healthz` endpoint
  - [ ] `/ready` endpoint

---

## Testing Migration

- [ ] Port-forward to Migration Coordinator API
- [ ] Identify target pod and node
- [ ] Register pod with coordinator (if needed)
- [ ] Trigger migration to different node
- [ ] Verify pod moved to target node
- [ ] Verify pod state preserved (connections, memory)

---

## Cleanup

- [ ] Run `make undeploy NAMESPACE=mig-ready` when done
- [ ] Verify all resources removed: `kubectl get all -n mig-ready`
- [ ] Delete namespace (optional): `kubectl delete namespace mig-ready`
- [ ] Stop Minikube (if used): `minikube stop`

---

## Troubleshooting Log

Document any issues encountered during deployment:

| Issue | Commands Run | Resolution |
|-------|-------------|-----------|
| | | |
| | | |
| | | |

---

## Next Steps after Successful Deployment

1. [ ] Review the Migration Coordinator logs for insights
2. [ ] Test the migration API endpoints
3. [ ] Modify example app configuration (if needed)
4. [ ] Integrate with custom applications
5. [ ] Scale to production cluster(s)
6. [ ] Monitor migration performance metrics

