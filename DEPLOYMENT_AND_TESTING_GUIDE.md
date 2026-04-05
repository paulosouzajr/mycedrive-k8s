# MyceDrive Deployment and Testing Guide

This guide provides step-by-step instructions for deploying and testing MyceDrive in a Kubernetes cluster.

## Table of Contents

1. [Prerequisites](#prerequisites)
2. [Overview](#overview)
3. [Local Development (Minikube)](#local-development-minikube)
4. [Production Cluster Deployment](#production-cluster-deployment)
5. [Running Unit Tests](#running-unit-tests)
6. [Verifying the Deployment](#verifying-the-deployment)
7. [Testing Migration](#testing-migration)
8. [Troubleshooting](#troubleshooting)

---

## Prerequisites

Verify you have all required tools installed:

```bash
# Check Go version (1.18+)
go version

# Check Docker
docker --version

# Check kubectl
kubectl version --client

# Check Helm
helm version

# Check minikube (for local testing)
minikube version

# Verify Kubernetes cluster access
kubectl cluster-info
```

**Required credentials:**
- Docker Hub account with access to the `mycedrive` organization (or use pre-built public images)
- `kubectl` configured to access your target cluster

---

## Overview

MyceDrive consists of three main components:

| Component | Role | Image |
|-----------|------|-------|
| **Migration Coordinator (go-server)** | REST API orchestrating migrations | `mycedrive/go-server:dev` |
| **Execution Agent (go-agent)** | Runs inside app containers, manages DMTCP | `mycedrive/go-agent:dev` |
| **DMTCP Sidecar** | Provides DMTCP binaries and checkpoint storage | `mycedrive/dmtcp:dev` |

**Deployment architecture:**
- Migration Coordinator runs once per cluster (deployed via Helm)
- DMTCP DaemonSet runs on every node (raw Kubernetes manifest)
- Application pods contain the Execution Agent + DMTCP sidecar

---

## Local Development (Minikube)

You can either build images locally or use pre-built images from Docker Hub.

### Step 1: Start Minikube

```bash
# Start minikube with sufficient resources
minikube start --cpus=4 --memory=8192

# Verify cluster is running
kubectl cluster-info
```

### Step 2: Prepare Images for Minikube

**Option A: Build images locally in Minikube:**

```bash
# From repository root
make minikube-build

# This runs:
# - docker build -t mycedrive/go-agent:dev ./go-agent
# - docker build -t mycedrive/go-server:dev ./go-server
# - docker build -t mycedrive/dmtcp:dev ./dmtcp
```

**Option B: Pull pre-built images from Docker Hub:**

```bash
# Pull latest dev images
make prepare-images PULL_IMAGES=true

# Or pull a specific version/tag
make prepare-images PULL_IMAGES=true TAG=v1.0.0

# Then load into minikube
make minikube-setup NAMESPACE=mig-ready
```

### Step 3: Create Namespace and Load Images

```bash
# Create the migration-ready namespace and load images into minikube
# (Required for Option A; Option B already loads images above)
make minikube-setup NAMESPACE=mig-ready

# This:
# - Creates the mig-ready namespace
# - Loads images into minikube
```

### Step 4: Deploy to Minikube

```bash
make deploy NAMESPACE=mig-ready TAG=dev

# This deploys:
# - Migration Coordinator (Helm)
# - DMTCP DaemonSet
# - Example Mosquitto application + DMTCP sidecar
```

### Step 5: Verify Deployment

```bash
# Check all pods are running
kubectl get pods -n mig-ready

# Expected output:
# NAME                                            READY   STATUS
# mycedrive-xxx                                   1/1     Running
# dmtcp-daemonset-xxxxxx                         1/1     Running
# mosquitto-with-dmtcp-sidecar-xxxxxxxxxx        2/2     Running
```

---

## Production Cluster Deployment

### Step 1: Build and Push Images to Docker Hub

```bash
# Login to Docker Hub
make login
# Prompts for username (must be mycedrive org member) and password

# Build all images
make build

# Push to Docker Hub
make push

# Or do it in one command:
# make login build push
```

### Step 2: Prepare Images

Before deploying, ensure images are available locally or in a registry:

**Build images locally:**
```bash
make build
# Or for registry: make build && make push
```

**Or pull pre-built images from DockerHub:**
```bash
make prepare-images PULL_IMAGES=true

# This is useful when:
# - You've already pushed images to your registry
# - You want to use released versions
# - You don't have local build dependencies
```

### Step 3: Deploy to Kubernetes Cluster

Once images are ready, deploy to your cluster:

```bash
# Deploy with default namespace (mig-ready) and tag (dev)
make deploy NAMESPACE=mig-ready TAG=dev

# Or customize:
make deploy NAMESPACE=production TAG=v1.0.0
```

The deploy command will:
- Run `helm upgrade --install` for Migration Coordinator
- Apply `kubectl` for DMTCP DaemonSet
- Apply `kubectl` for example Mosquitto application

### Step 4: Verify Pods Are Running

```bash
kubectl get pods -n mig-ready

# Check logs
kubectl logs -n mig-ready -l app=mycedrive
kubectl logs -n mig-ready -l app=dmtcp-daemonset
```

---

## Running Unit Tests

### Test Without Cluster

All unit tests run locally without requiring a Kubernetes cluster:

```bash
# Run all tests for both go-agent and go-server
make test

# Output includes:
# - go-agent/overlay tests (overlayfs layer management)
# - go-agent/dmtcp tests (DMTCP lifecycle)
# - go-server tests (API handlers)
```

### Run Tests Individually

```bash
# Test execution agent only
cd go-agent && go test ./...

# Test migration coordinator only
cd go-server && go test ./...

# Run with verbose output
cd go-agent && go test -v ./...
```

---

## Verifying the Deployment

### Check Health Endpoints

```bash
# Port-forward the Migration Coordinator
kubectl port-forward -n mig-ready svc/mycedrive 8080:80 &

# Test health checks
curl http://localhost:8080/healthz
# Returns: {"status":"ok"}

curl http://localhost:8080/ready
# Returns: {"status":"ready"}
```

### Check DaemonSet Spread

```bash
# Verify DMTCP runs on all nodes
kubectl get pods -n mig-ready -l app=dmtcp-daemonset -o wide

# Each node should have one dmtcp-daemonset pod
```

### Check Service Account and RBAC

```bash
# Verify RBAC is created
kubectl get clusterrole mycedrive -o yaml

# Verify service account has permissions
kubectl describe clusterrolebinding mycedrive
```

---

## Testing Migration

### Manual Migration Test

1. **Get the example Mosquitto pod:**
   ```bash
   kubectl get pods -n mig-ready -o wide
   # Note the pod name and current node
   ```

2. **Trigger a checkpoint/migration via the API:**
   ```bash
   # Port-forward to Migration Coordinator
   kubectl port-forward -n mig-ready svc/mycedrive 8080:80 &
   
   # Register pod first (if not auto-registered)
   curl -X POST http://localhost:8080/register \
     -H "Content-Type: application/json" \
     -d '{"pod":"mosquitto-xxx","namespace":"mig-ready"}'
   
   # Trigger migration
   curl -X POST http://localhost:8080/migrate \
     -H "Content-Type: application/json" \
     -d '{"pod":"mosquitto-xxx","namespace":"mig-ready","target_node":"node-2"}'
   ```

3. **Verify pod moved to target node:**
   ```bash
   kubectl get pods -n mig-ready -o wide
   # Check that the pod's NODE column changed
   ```

### Integration Test Output

After migration completes:
- Pod should be running on the target node
- Open connections should be preserved
- Pod should recover from saved checkpoint state

---

## Cleanup

### Remove All Deployed Resources

```bash
# Tear down everything
make undeploy NAMESPACE=mig-ready

# This removes:
# - Example application pods
# - DMTCP DaemonSet
# - Migration Coordinator (Helm release)
```

### Stop Minikube

```bash
minikube stop

# Or delete entirely:
minikube delete
```

---

## Troubleshooting

### 1. Pods Not Starting

```bash
# Check pod status
kubectl describe pod <pod-name> -n mig-ready

# Check logs
kubectl logs <pod-name> -n mig-ready

# Check events
kubectl get events -n mig-ready --sort-by='.lastTimestamp'
```

### 2. Image Pull Errors

**Minikube:**
```bash
# Rebuild and reload image
eval $(minikube docker-env)
docker build -t mycedrive/go-server:dev ./go-server
# (no need to push for minikube)
```

**Production:**
```bash
# Verify image exists in Docker Hub
docker pull mycedrive/go-server:dev

# Check imagePullSecrets if using private registry
kubectl get secret -n mig-ready
```

### 3. RBAC Permission Denied

```bash
# Check service account exists
kubectl get serviceaccount -n mig-ready

# Check ClusterRole and ClusterRoleBinding
kubectl get clusterrolebinding mycedrive
kubectl get clusterrole mycedrive

# Re-apply deployment to update RBAC
make deploy-coordinator NAMESPACE=mig-ready TAG=dev
```

### 4. Helm Values Not Applied

```bash
# Check current Helm release values
helm get values mycedrive -n mig-ready

# Upgrade Helm release with new values
helm upgrade mycedrive deployment/myce-server/mycedrive \
  -n mig-ready \
  --set image.tag=v1.0.0 \
  --set image.pullPolicy=Always
```

### 5. DaemonSet Not Rolling Out

```bash
# Check if DaemonSet is scheduled correctly
kubectl describe daemonset dmtcp-daemonset -n mig-ready

# Check for node selectors or taints
kubectl get nodes --show-labels
```

### 6. Test Failures

```bash
# Run tests with more verbosity
cd go-agent && go test -v -race ./...

# Check for system-level requirements (overlay, dmtcp binaries)
which dmtcp_launch
which dmtcp_coordinator
```

---

## Common Makefile Commands Reference

```bash
# Info & Testing
make test              # Run all unit tests
make build             # Build all Docker images
make build-agent       # Build go-agent only
make build-server      # Build go-server only
make build-dmtcp       # Build dmtcp sidecar only

# Minikube Workflow
make minikube-build    # Build inside minikube docker
make minikube-setup    # Create namespace and load images

# Image Preparation (separate from deployment)
make prepare-images       # Build or pull images (configure with PULL_IMAGES=true)
make prepare-images PULL_IMAGES=true  # Pull images from registry

# Deployment (assumes images are ready)
make deploy            # Deploy all (Helm + kubectl manifests)
make deploy-coordinator  # Deploy just Migration Coordinator (Helm)
make deploy-daemonset  # Deploy just DMTCP DaemonSet
make deploy-app        # Deploy just example app
make deploy-full       # Alternative: deploy using full stack manifest

# Cleanup
make undeploy          # Remove all deployed resources
make login             # Docker Hub login

# Variables you can override
# make build TAG=latest
# make prepare-images PULL_IMAGES=true  # Pull instead of build
# make deploy NAMESPACE=prod TAG=v1.0
# make deploy DOCKERHUB_ORG=mycedrive TAG=v1.0  # Applies go-server and dmtcp image tags
```

---

## Next Steps

1. **Complete unit tests:** Run `make test` to ensure all components compile and pass tests
2. **Deploy to Minikube:** Follow the [Local Development](#local-development-minikube) section for quick testing
3. **Run integration tests:** Deploy and trigger migrations as described in [Testing Migration](#testing-migration)
4. **Deploy to production:** Push images to Docker Hub and deploy to your cluster following [Production Deployment](#production-cluster-deployment)
5. **Monitor and debug:** Use the troubleshooting section when issues arise

