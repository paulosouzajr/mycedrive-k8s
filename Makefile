##############################################################################
# MyceDrive – Build, Push and Deploy
#
# Prerequisites:
#   - Docker (buildx recommended for multi-arch)
#   - kubectl configured against your target cluster
#   - helm v3
#   - Docker Hub access to the 'mycedrive' organisation
#
# Quick start (minikube – no push needed):
#   make minikube-build
#   make deploy
#
# Quick start (Docker Hub):
#   make login                   # prompts for credentials
#   make build push
#   make deploy
##############################################################################

DOCKERHUB_ORG ?= mycedrive
TAG        ?= dev
NAMESPACE  ?= mig-ready
HELM_CHART  = deployment/myce-server/mycedrive
HELM_REL    = mycedrive

# Docker Hub image references  (docker.io/mycedrive/<name>:<tag>)
IMG_SERVER  = $(DOCKERHUB_ORG)/go-server:$(TAG)
IMG_AGENT   = $(DOCKERHUB_ORG)/go-agent:$(TAG)
IMG_DMTCP   = $(DOCKERHUB_ORG)/dmtcp:$(TAG)

.PHONY: all build push deploy undeploy \
        build-server build-agent build-dmtcp \
        push-server push-agent push-dmtcp \
        login minikube-setup minikube-build test lint clean

##############################################################################
# Default
##############################################################################
all: build

##############################################################################
# Docker Hub login
##############################################################################
login:
	docker login --username $(DOCKERHUB_ORG)

##############################################################################
# Build images
##############################################################################
build: build-dmtcp build-agent build-server

build-server:
	@echo "==> Building Migration Coordinator (go-server) → $(IMG_SERVER)"
	docker build \
	  -t $(IMG_SERVER) \
	  ./go-server

build-agent:
	@echo "==> Building Execution Agent (go-agent) → $(IMG_AGENT)"
	docker build \
	  -t $(IMG_AGENT) \
	  ./go-agent

build-dmtcp:
	@echo "==> Building DMTCP image → $(IMG_DMTCP)"
	docker build \
	  -t $(IMG_DMTCP) \
	  ./dmtcp

##############################################################################
# Push images
##############################################################################
push: push-dmtcp push-agent push-server

push-server:
	docker push $(IMG_SERVER)

push-agent:
	docker push $(IMG_AGENT)

push-dmtcp:
	docker push $(IMG_DMTCP)

##############################################################################
# Minikube helpers
# Loads images directly into minikube so no registry is needed.
##############################################################################
minikube-setup:
	@echo "==> Creating namespace $(NAMESPACE)"
	kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -
	@echo "==> Loading images into minikube"
	minikube image load $(IMG_SERVER)
	minikube image load $(IMG_AGENT)
	minikube image load $(IMG_DMTCP)

minikube-build:
	@echo "==> Building images inside minikube Docker daemon"
	eval $$(minikube docker-env) && \
	  docker build -t $(IMG_DMTCP)   ./dmtcp  && \
	  docker build -t $(IMG_AGENT)   ./go-agent && \
	  docker build -t $(IMG_SERVER)  ./go-server

##############################################################################
# Deploy – Helm (Migration Coordinator) + raw manifests (DMTCP DaemonSet)
##############################################################################
deploy: deploy-coordinator deploy-daemonset deploy-app

deploy-coordinator:
	@echo "==> Installing/upgrading Migration Coordinator via Helm"
	helm upgrade --install $(HELM_REL) $(HELM_CHART) \
	  --namespace $(NAMESPACE) \
	  --create-namespace \
	  --set image.repository=$(DOCKERHUB_ORG)/go-server \
	  --set image.tag=$(TAG) \
	  --set image.pullPolicy=IfNotPresent \
	  --wait

deploy-daemonset:
	@echo "==> Applying DMTCP DaemonSet"
	kubectl apply -f dmtcp/dmtcp-daemonset.yml --namespace $(NAMESPACE)

deploy-app:
	@echo "==> Applying example application (mosquitto + DMTCP sidecar)"
	kubectl apply -f dmtcp/deploy_sidecar.yml --namespace $(NAMESPACE)

##############################################################################
# Full stack deploy (alternative to Helm for the prototype)
##############################################################################
deploy-full:
	@echo "==> Applying full stack manifest"
	kubectl apply -f dmtcp/deploy_full_stack.yml

##############################################################################
# Teardown
##############################################################################
undeploy:
	@echo "==> Removing application and DaemonSet"
	-kubectl delete -f dmtcp/deploy_sidecar.yml   --namespace $(NAMESPACE) --ignore-not-found
	-kubectl delete -f dmtcp/dmtcp-daemonset.yml   --namespace $(NAMESPACE) --ignore-not-found
	@echo "==> Uninstalling Helm release"
	-helm uninstall $(HELM_REL) --namespace $(NAMESPACE)

##############################################################################
# Test
##############################################################################
test:
	@echo "==> Running go-agent tests"
	cd go-agent && go test ./...
	@echo "==> Running go-server tests"
	cd go-server && go test ./...

##############################################################################
# Lint / vet
##############################################################################
lint:
	cd go-agent  && go vet ./...
	cd go-server && go vet ./...

##############################################################################
# Clean local build artefacts
##############################################################################
clean:
	-docker rmi $(IMG_SERVER) $(IMG_AGENT) $(IMG_DMTCP) 2>/dev/null || true
