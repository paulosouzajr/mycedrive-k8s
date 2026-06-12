package restapi

import (
	"net/http"
	"time"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
)

// legacyPod is the /pods item shape the existing dashboard consumes.
type legacyPod struct {
	PodName    string `json:"podName"`
	PodAddress string `json:"podAddress"`
	Migrating  bool   `json:"migrating"`
}

// apiPod is the richer /api/v1/pods item shape.
type apiPod struct {
	Name             string     `json:"name"`
	Address          string     `json:"address,omitempty"`
	ContainerPort    int        `json:"containerPort,omitempty"`
	Node             string     `json:"node,omitempty"`
	Workload         string     `json:"workload,omitempty"`
	Namespace        string     `json:"namespace,omitempty"`
	Migrating        bool       `json:"migrating"`
	CheckpointReady  bool       `json:"checkpointReady"`
	Restored         bool       `json:"restored"`
	ProcessMigration bool       `json:"processMigration"`
	VolumeMigration  bool       `json:"volumeMigration"`
	SyncRound        int        `json:"syncRound,omitempty"`
	SyncRounds       int        `json:"syncRounds,omitempty"`
	RegisteredAt     *time.Time `json:"registeredAt,omitempty"`
	LastSeen         *time.Time `json:"lastSeen,omitempty"`
}

// apiMigration is the /api/v1/migrations item shape.
type apiMigration struct {
	Name             string     `json:"name"`
	Namespace        string     `json:"namespace"`
	Workload         string     `json:"workload"`
	PodName          string     `json:"podName,omitempty"`
	SourceNode       string     `json:"sourceNode"`
	TargetNode       string     `json:"targetNode"`
	Phase            string     `json:"phase"`
	Message          string     `json:"message,omitempty"`
	SourcePod        string     `json:"sourcePod,omitempty"`
	DestinationPod   string     `json:"destinationPod,omitempty"`
	ProcessMigration bool       `json:"processMigration"`
	VolumeMigration  bool       `json:"volumeMigration"`
	SyncRound        int32      `json:"syncRound,omitempty"`
	SyncRounds       int32      `json:"syncRounds,omitempty"`
	StartTime        *time.Time `json:"startTime,omitempty"`
	CompletionTime   *time.Time `json:"completionTime,omitempty"`
}

// handleLegacyPods implements GET /pods (legacy dashboard shape).
func (s *Server) handleLegacyPods(w http.ResponseWriter, _ *http.Request) {
	records := s.Registry.List()
	out := make([]legacyPod, 0, len(records))
	for _, rec := range records {
		out = append(out, legacyPod{PodName: rec.Name, PodAddress: rec.Address, Migrating: rec.Migrating})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleAPIPods implements GET /api/v1/pods.
func (s *Server) handleAPIPods(w http.ResponseWriter, _ *http.Request) {
	records := s.Registry.List()
	out := make([]apiPod, 0, len(records))
	for _, rec := range records {
		registeredAt, lastSeen := rec.RegisteredAt, rec.LastSeen
		p := apiPod{
			Name:             rec.Name,
			Address:          rec.Address,
			ContainerPort:    rec.ContainerPort,
			Node:             rec.Node,
			Workload:         rec.WorkloadName,
			Namespace:        rec.WorkloadNamespace,
			Migrating:        rec.Migrating,
			CheckpointReady:  rec.CheckpointReady,
			Restored:         rec.Restored,
			ProcessMigration: rec.ProcessMigration,
			VolumeMigration:  rec.VolumeMigration,
			SyncRound:        rec.SyncRound,
			SyncRounds:       rec.SyncRounds,
		}
		if !registeredAt.IsZero() {
			p.RegisteredAt = &registeredAt
		}
		if !lastSeen.IsZero() {
			p.LastSeen = &lastSeen
		}
		out = append(out, p)
	}
	writeJSON(w, http.StatusOK, map[string]any{"pods": out})
}

// handleAPIMigrations implements GET /api/v1/migrations.
func (s *Server) handleAPIMigrations(w http.ResponseWriter, r *http.Request) {
	var list mycedrivev1alpha1.MigrationList
	if err := s.Client.List(r.Context(), &list); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	out := make([]apiMigration, 0, len(list.Items))
	for i := range list.Items {
		mig := &list.Items[i]
		m := apiMigration{
			Name:             mig.Name,
			Namespace:        mig.Namespace,
			Workload:         mig.Spec.WorkloadName,
			PodName:          mig.Spec.PodName,
			SourceNode:       mig.Spec.SourceNode,
			TargetNode:       mig.Spec.TargetNode,
			Phase:            string(mig.Status.Phase),
			Message:          mig.Status.Message,
			SourcePod:        mig.Status.SourcePod,
			DestinationPod:   mig.Status.DestinationPod,
			ProcessMigration: mig.Status.ProcessMigration,
			VolumeMigration:  mig.Status.VolumeMigration,
			SyncRound:        mig.Status.SyncRound,
			SyncRounds:       mig.Status.SyncRounds,
		}
		if m.Phase == "" {
			m.Phase = "Pending"
		}
		if mig.Status.StartTime != nil {
			t := mig.Status.StartTime.Time
			m.StartTime = &t
		}
		if mig.Status.CompletionTime != nil {
			t := mig.Status.CompletionTime.Time
			m.CompletionTime = &t
		}
		out = append(out, m)
	}
	writeJSON(w, http.StatusOK, map[string]any{"migrations": out})
}
