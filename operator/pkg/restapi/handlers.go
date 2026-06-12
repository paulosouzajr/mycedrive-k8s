package restapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
)

// Message is the /register payload. The request fields are byte-compatible
// with the legacy go-server contract; processMigration, volumeMigration,
// checkpointDir and syncRounds are additive response fields for the fixed
// Execution Agent.
type Message struct {
	PodName       string `json:"podName"`
	PodAddress    string `json:"podAddress"`
	ContainerPort int    `json:"containerPort,omitempty"`
	IsNew         bool   `json:"isNew"`
	IsMig         bool   `json:"isMig"`

	ProcessMigration bool   `json:"processMigration"`
	VolumeMigration  bool   `json:"volumeMigration"`
	CheckpointDir    string `json:"checkpointDir,omitempty"`
	SyncRounds       int    `json:"syncRounds,omitempty"`
}

// RemoveRequest / RemoveResponse implement POST /remove.
type RemoveRequest struct {
	PodName string `json:"podName"`
}

type RemoveResponse struct {
	NeedsCheckpoint  bool `json:"needsCheckpoint"`
	ProcessMigration bool `json:"processMigration"`
	VolumeMigration  bool `json:"volumeMigration"`

	// DestAddress (additive, optional) is the host:port the migration-target
	// EA listens on, so the source can stream checkpoints directly over TCP.
	// Empty when no target has registered yet; the agent then keeps the
	// checkpoints local for MC-driven copy.
	DestAddress string `json:"destAddress,omitempty"`
}

// CopyNotification implements POST /copy.
type CopyNotification struct {
	PodName       string `json:"podName"`
	CheckpointDir string `json:"checkpointDir"`
}

// SyncNotification implements POST /sync (additive: pre-downtime overlay
// snapshot round completed by the source EA).
type SyncNotification struct {
	PodName string `json:"podName"`
	Round   int    `json:"round"`
}

// RestoredNotification implements POST /restored (additive: destination EA
// finished dmtcp_restart).
type RestoredNotification struct {
	PodName string `json:"podName"`
}

// MigrateRequest accepts both the legacy shape (deployment/originNode/
// destNode/label) and the new shape (workload/podName/sourceNode/targetNode/
// namespace).
type MigrateRequest struct {
	Deployment string `json:"deployment,omitempty"`
	OriginNode string `json:"originNode,omitempty"`
	DestNode   string `json:"destNode,omitempty"`
	Label      string `json:"label,omitempty"`

	Workload   string `json:"workload,omitempty"`
	Namespace  string `json:"namespace,omitempty"`
	PodName    string `json:"podName,omitempty"`
	SourceNode string `json:"sourceNode,omitempty"`
	TargetNode string `json:"targetNode,omitempty"`
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	var msg Message
	if !decodeJSON(w, r, &msg) {
		return
	}
	if msg.PodName == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "podName is required"})
		return
	}

	prev, isNew := s.Registry.Register(msg.PodName, msg.PodAddress, msg.ContainerPort)
	rec, _ := s.Registry.Get(msg.PodName)

	if isNew {
		writeJSON(w, http.StatusCreated, Message{
			PodName:          msg.PodName,
			PodAddress:       msg.PodAddress,
			IsNew:            true,
			IsMig:            false,
			ProcessMigration: rec.ProcessMigration,
			VolumeMigration:  rec.VolumeMigration,
		})
		return
	}

	// Duplicate name: either the destination EA of an active migration
	// (isMig=true: block and wait for the checkpoint) or a plain restart.
	writeJSON(w, http.StatusOK, Message{
		PodName:          msg.PodName,
		PodAddress:       prev.Address,
		ContainerPort:    msg.ContainerPort,
		IsNew:            false,
		IsMig:            rec.Migrating,
		ProcessMigration: rec.ProcessMigration,
		VolumeMigration:  rec.VolumeMigration,
		CheckpointDir:    rec.CheckpointDir,
		SyncRounds:       rec.SyncRounds,
	})
}

func (s *Server) handleRemove(w http.ResponseWriter, r *http.Request) {
	var req RemoveRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	rec, known := s.Registry.Get(req.PodName)
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("pod %q not registered", req.PodName)})
		return
	}
	resp := RemoveResponse{
		NeedsCheckpoint:  rec.Migrating,
		ProcessMigration: rec.ProcessMigration,
		VolumeMigration:  rec.VolumeMigration,
	}
	if rec.Migrating {
		resp.DestAddress = rec.DestAddress
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleCopy(w http.ResponseWriter, r *http.Request) {
	var notif CopyNotification
	if !decodeJSON(w, r, &notif) {
		return
	}
	if !s.Registry.MarkCheckpointReady(notif.PodName, notif.CheckpointDir) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("pod %q not registered", notif.PodName)})
		return
	}
	s.Log.Info("checkpoint acknowledged", "pod", notif.PodName, "dir", notif.CheckpointDir)
	writeJSON(w, http.StatusOK, map[string]string{"status": "copy_initiated", "pod": notif.PodName})
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request) {
	var notif SyncNotification
	if !decodeJSON(w, r, &notif) {
		return
	}
	if !s.Registry.RecordSyncRound(notif.PodName, notif.Round) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("pod %q not registered", notif.PodName)})
		return
	}
	rec, _ := s.Registry.Get(notif.PodName)
	remaining := rec.SyncRounds - rec.SyncRound
	if remaining < 0 {
		remaining = 0
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "sync_recorded", "pod": notif.PodName, "round": rec.SyncRound, "remaining": remaining})
}

func (s *Server) handleRestored(w http.ResponseWriter, r *http.Request) {
	var notif RestoredNotification
	if !decodeJSON(w, r, &notif) {
		return
	}
	if !s.Registry.MarkRestored(notif.PodName) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("pod %q not registered", notif.PodName)})
		return
	}
	s.Log.Info("restore acknowledged", "pod", notif.PodName)
	writeJSON(w, http.StatusOK, map[string]string{"status": "restored", "pod": notif.PodName})
}

// handlePoll lets a running source EA discover an armed migration:
// GET /poll?podName=NAME.
func (s *Server) handlePoll(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("podName")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "podName query parameter is required"})
		return
	}
	rec, known := s.Registry.Get(name)
	if !known {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": fmt.Sprintf("pod %q not registered", name)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"podName":          rec.Name,
		"migrating":        rec.Migrating,
		"processMigration": rec.ProcessMigration,
		"volumeMigration":  rec.VolumeMigration,
		"checkpointDir":    rec.CheckpointDir,
		"syncRounds":       rec.SyncRounds,
		"syncRound":        rec.SyncRound,
	})
}

func (s *Server) handleMigrate(w http.ResponseWriter, r *http.Request) {
	var req MigrateRequest
	if !decodeJSON(w, r, &req) {
		return
	}

	workload := req.Workload
	if workload == "" {
		workload = req.Deployment
	}
	sourceNode := req.SourceNode
	if sourceNode == "" {
		sourceNode = req.OriginNode
	}
	targetNode := req.TargetNode
	if targetNode == "" {
		targetNode = req.DestNode
	}
	namespace := req.Namespace
	if namespace == "" {
		namespace = s.DefaultNamespace
	}
	if workload == "" || sourceNode == "" || targetNode == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "workload (or deployment), sourceNode (or originNode) and targetNode (or destNode) are required"})
		return
	}

	ctx := r.Context()
	if _, err := s.ensureMigratableWorkload(ctx, namespace, workload, req.Label); err != nil {
		code := http.StatusInternalServerError
		if apierrors.IsNotFound(err) || strings.Contains(err.Error(), "no StatefulSet or Deployment") {
			code = http.StatusNotFound
		}
		writeJSON(w, code, map[string]string{"error": err.Error()})
		return
	}

	mig := &mycedrivev1alpha1.Migration{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: workload + "-",
			Namespace:    namespace,
		},
		Spec: mycedrivev1alpha1.MigrationSpec{
			WorkloadName: workload,
			PodName:      req.PodName,
			SourceNode:   sourceNode,
			TargetNode:   targetNode,
		},
	}
	if err := s.Client.Create(ctx, mig); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	s.Log.Info("migration created", "migration", mig.Name, "workload", workload, "source", sourceNode, "target", targetNode)
	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "migration_started",
		"deployment": workload, // legacy key
		"workload":   workload,
		"migration":  mig.Name,
		"namespace":  namespace,
	})
}

// ensureMigratableWorkload returns the MigratableWorkload for name, creating
// one (kind auto-detected, StatefulSet preferred) for legacy /migrate callers
// that predate the CRDs.
func (s *Server) ensureMigratableWorkload(ctx context.Context, namespace, name, label string) (*mycedrivev1alpha1.MigratableWorkload, error) {
	mw := &mycedrivev1alpha1.MigratableWorkload{}
	err := s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, mw)
	if err == nil {
		return mw, nil
	}
	if !apierrors.IsNotFound(err) {
		return nil, err
	}

	kind := ""
	var sts appsv1.StatefulSet
	if s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &sts) == nil {
		kind = mycedrivev1alpha1.WorkloadKindStatefulSet
	} else {
		var dep appsv1.Deployment
		if s.Client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &dep) == nil {
			kind = mycedrivev1alpha1.WorkloadKindDeployment
		}
	}
	if kind == "" {
		return nil, fmt.Errorf("no StatefulSet or Deployment named %q in namespace %q", name, namespace)
	}

	mw = &mycedrivev1alpha1.MigratableWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: mycedrivev1alpha1.MigratableWorkloadSpec{
			WorkloadRef: mycedrivev1alpha1.WorkloadReference{Kind: kind, Name: name},
		},
	}
	// Legacy "key:value" label override.
	if parts := strings.SplitN(label, ":", 2); label != "" && len(parts) == 2 {
		mw.Spec.PlacementLabel = &mycedrivev1alpha1.PlacementLabel{Key: parts[0], Value: parts[1]}
	}
	if err := s.Client.Create(ctx, mw); err != nil {
		if apierrors.IsAlreadyExists(err) {
			return mw, nil
		}
		return nil, err
	}
	s.Log.Info("auto-created MigratableWorkload", "name", name, "namespace", namespace, "kind", kind)
	return mw, nil
}
