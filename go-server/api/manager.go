package api

import (
	"fmt"
	"go-client/kub"
	"go-client/logs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

// pod tracks a registered application container.
type pod struct {
	mig        bool
	podName    string
	podAddress string
}

// Message is the JSON payload exchanged between the Execution Agent and the
// Migration Coordinator on /register.
type Message struct {
	PodName       string `json:"podName"`
	PodAddress    string `json:"podAddress"`
	ContainerPort int    `json:"containerPort,omitempty"`
	IsNew         bool   `json:"isNew"`
	IsMig         bool   `json:"isMig"`
}

// RemoveRequest is sent by the EA from the container preStop hook.
type RemoveRequest struct {
	PodName string `json:"podName"`
}

// RemoveResponse tells the EA whether to produce a DMTCP checkpoint before stopping.
type RemoveResponse struct {
	NeedsCheckpoint bool `json:"needsCheckpoint"`
}

// CopyNotification is sent by the source EA once checkpoint files are ready.
type CopyNotification struct {
	PodName       string `json:"podName"`
	CheckpointDir string `json:"checkpointDir"`
}

// MigrateRequest is the JSON body for POST /migrate.
type MigrateRequest struct {
	Deployment string `json:"deployment"`
	OriginNode string `json:"originNode"`
	DestNode   string `json:"destNode"`
	Label      string `json:"label"`
}

var pods []pod

// RegisterPod handles POST /register.
//
// A container calls this when it starts. If the coordinator already holds a
// record with the same pod name, a migration replica has started on the
// destination node: the response carries IsMig=true so the EA blocks until
// the checkpoint transfer is complete.
func RegisterPod(c *gin.Context) {
	var msg Message
	if err := c.BindJSON(&msg); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	for _, p := range pods {
		if p.podName == msg.PodName {
			c.JSON(http.StatusOK, Message{
				PodName:       msg.PodName,
				PodAddress:    p.podAddress,
				ContainerPort: msg.ContainerPort,
				IsNew:         false,
				IsMig:         p.mig,
			})
			return
		}
	}

	pods = append(pods, pod{podName: msg.PodName, podAddress: msg.PodAddress})
	c.JSON(http.StatusCreated, Message{
		PodName:    msg.PodName,
		PodAddress: msg.PodAddress,
		IsNew:      true,
		IsMig:      false,
	})
}

// RemovePod handles POST /remove.
//
// Called by the EA from the container preStop hook. Returns whether the EA
// must produce a DMTCP checkpoint before the container stops. A checkpoint
// is only required when the pod has been marked for migration.
func RemovePod(c *gin.Context) {
	var req RemoveRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	for i, p := range pods {
		if p.podName == req.PodName {
			c.JSON(http.StatusOK, RemoveResponse{NeedsCheckpoint: pods[i].mig})
			return
		}
	}

	c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("pod %q not registered", req.PodName)})
}

// CopyCheckpoint handles POST /copy.
//
// The source EA calls this after writing checkpoint files to disk. The
// coordinator marks the pod so that when the destination EA calls /register
// it receives IsMig=true and knows the checkpoint is on the way.
//
// In the prototype the byte transfer is performed by the EA pair directly
// over TCP (SendFile / ReceiveData). This endpoint is the coordination
// signal that gates the destination EA.
func CopyCheckpoint(c *gin.Context) {
	var notif CopyNotification
	if err := c.BindJSON(&notif); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}

	found := false
	for i := range pods {
		if pods[i].podName == notif.PodName {
			pods[i].mig = true
			found = true
			break
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("pod %q not registered", notif.PodName)})
		return
	}

	logs.LogInfo(fmt.Sprintf("checkpoint acknowledged: pod=%s dir=%s", notif.PodName, notif.CheckpointDir))
	c.JSON(http.StatusOK, gin.H{"status": "copy_initiated", "pod": notif.PodName})
}

// MigratePod handles POST /migrate.
//
// Accepts a JSON body: {"deployment", "originNode", "destNode", "label"}.
// label defaults to "mig-ready:true" and must follow the key:value format.
func MigratePod(c *gin.Context) {
	var req MigrateRequest
	if err := c.BindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body"})
		return
	}
	if containsEmpty(req.Deployment, req.OriginNode, req.DestNode) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "deployment, originNode and destNode are required"})
		return
	}
	if req.Label == "" {
		req.Label = "mig-ready:true"
	}

	found := false
	for i := range pods {
		if pods[i].podName == req.Deployment {
			pods[i].mig = true
			found = true
		}
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("deployment %q not registered", req.Deployment)})
		return
	}

	config, err := k8sConfig()
	if err != nil {
		logs.LogError(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	if err := migrate(config, req.Deployment, req.Label, req.OriginNode, req.DestNode); err != nil {
		logs.LogError(err)
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "migration_started", "deployment": req.Deployment})
}

// migrate performs the Kubernetes steps to move a deployment replica from
// currentNode to destNode:
//  1. Clears the placement label on the source node, sets it on the destination.
//  2. Scales up the deployment so Kubernetes creates the destination pod.
//  3. Deletes the source pod, triggering the preStop checkpoint flow in the EA.
func migrate(config *rest.Config, deployment, label, currentNode, destNode string) error {
	parts := strings.SplitN(label, ":", 2)
	if len(parts) != 2 {
		return fmt.Errorf("label %q must be key:value", label)
	}

	cs, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("kubernetes client: %w", err)
	}

	srcNode := kub.FindNode(cs, currentNode)
	dstNode := kub.FindNode(cs, destNode)
	kub.AddNodeLabels(cs, srcNode, map[string]string{parts[0]: ""})
	kub.AddNodeLabels(cs, dstNode, map[string]string{parts[0]: parts[1]})

	if !kub.ScaleUp(cs, deployment) {
		return fmt.Errorf("scale up failed for %q", deployment)
	}
	logs.LogInfo(fmt.Sprintf("scaled up deployment %q", deployment))

	if !kub.DeletePod(cs, deployment) {
		return fmt.Errorf("delete source pod failed for %q", deployment)
	}
	logs.LogInfo(fmt.Sprintf("source pod deleted for deployment %q", deployment))

	return nil
}

// k8sConfig returns in-cluster credentials when available, falling back to
// the local kubeconfig for development.
func k8sConfig() (*rest.Config, error) {
	cfg, err := rest.InClusterConfig()
	if err == nil {
		return cfg, nil
	}
	return clientcmd.BuildConfigFromFlags("", "../minikube/config")
}

func containsEmpty(ss ...string) bool {
	for _, s := range ss {
		if s == "" {
			return true
		}
	}
	return false
}
