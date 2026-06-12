package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Supported workload kinds.
const (
	WorkloadKindStatefulSet = "StatefulSet"
	WorkloadKindDeployment  = "Deployment"
)

// Defaults applied when the corresponding spec fields are empty.
const (
	DefaultPlacementLabelKey   = "mig-ready"
	DefaultPlacementLabelValue = "true"
	DefaultCheckpointDir       = "/dmtcp/checkpoints"
	DefaultTransferPort        = 2486
	DefaultLayerCount          = 1
	DefaultPreSyncRounds       = 1
)

// WorkloadReference points at the Kubernetes workload (in the same namespace
// as the MigratableWorkload) whose pods are wrapped by the Execution Agent.
type WorkloadReference struct {
	// Kind of the workload. StatefulSet is the primary supported target
	// because its pods keep stable names across rescheduling; Deployment is
	// supported on a best-effort basis.
	// +kubebuilder:validation:Enum=StatefulSet;Deployment
	Kind string `json:"kind"`

	// Name of the StatefulSet or Deployment.
	Name string `json:"name"`
}

// PlacementLabel is the node label used to steer scheduling during a
// migration. It must match the nodeSelector configured on the workload's pod
// template (make-migratable.sh uses mig-ready="true").
type PlacementLabel struct {
	// +kubebuilder:default=mig-ready
	Key string `json:"key,omitempty"`
	// +kubebuilder:default="true"
	Value string `json:"value,omitempty"`
}

// MigratableWorkloadSpec describes a workload under MyceDrive management.
type MigratableWorkloadSpec struct {
	// WorkloadRef identifies the wrapped StatefulSet or Deployment.
	WorkloadRef WorkloadReference `json:"workloadRef"`

	// PlacementLabel is the node label used to constrain pod placement
	// during migration. Defaults to mig-ready="true".
	// +optional
	PlacementLabel *PlacementLabel `json:"placementLabel,omitempty"`

	// CheckpointDir is the directory inside the pod where DMTCP writes
	// checkpoint images. Defaults to /dmtcp/checkpoints.
	// +optional
	CheckpointDir string `json:"checkpointDir,omitempty"`

	// TransferPort is the TCP port the Execution Agent listens on for
	// checkpoint/layer transfer. Defaults to 2486.
	// +optional
	TransferPort int32 `json:"transferPort,omitempty"`

	// LayerCount is the number of overlayfs layers the Execution Agent
	// exports during migration. Defaults to 1.
	// +optional
	LayerCount int32 `json:"layerCount,omitempty"`

	// ProcessMigration toggles DMTCP process-memory checkpoint/restore
	// (MyceDrive, ICFEC 2022). Defaults to true. Propagated to the
	// Execution Agent via the /register and /remove REST responses.
	// +optional
	ProcessMigration *bool `json:"processMigration,omitempty"`

	// VolumeMigration toggles overlayfs volume-layer checkpointing and
	// transfer (CloudCom 2020). Defaults to true. Propagated to the
	// Execution Agent via the /register and /remove REST responses.
	// +optional
	VolumeMigration *bool `json:"volumeMigration,omitempty"`

	// PreSyncRounds is the number of iterative overlay snapshot transfer
	// rounds performed while the source pod is still running (before
	// downtime), per the CloudCom 2020 flow. Only used when
	// VolumeMigration is enabled. Defaults to 1; 0 disables pre-sync.
	// +optional
	PreSyncRounds *int32 `json:"preSyncRounds,omitempty"`
}

// RegisteredPod mirrors one Execution Agent registration from the operator's
// in-memory registry into the CRD status so state survives operator restarts.
type RegisteredPod struct {
	Name    string `json:"name"`
	Address string `json:"address,omitempty"`
	// +optional
	ContainerPort int32 `json:"containerPort,omitempty"`
	// +optional
	Node string `json:"node,omitempty"`
	// +optional
	Migrating bool `json:"migrating,omitempty"`
	// +optional
	CheckpointReady bool `json:"checkpointReady,omitempty"`
	// +optional
	CheckpointDir string `json:"checkpointDir,omitempty"`
	// +optional
	Restored bool `json:"restored,omitempty"`
	// +optional
	RegisteredAt *metav1.Time `json:"registeredAt,omitempty"`
}

// MigratableWorkloadStatus is the observed state of a MigratableWorkload.
type MigratableWorkloadStatus struct {
	// Phase is Ready when the referenced workload exists, Pending otherwise.
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// RegisteredPods lists Execution Agent registrations for this workload.
	// +optional
	RegisteredPods []RegisteredPod `json:"registeredPods,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mw
// +kubebuilder:printcolumn:name="Kind",type=string,JSONPath=`.spec.workloadRef.kind`
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadRef.name`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// MigratableWorkload represents a wrapped workload under MyceDrive management.
type MigratableWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MigratableWorkloadSpec   `json:"spec,omitempty"`
	Status MigratableWorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MigratableWorkloadList contains a list of MigratableWorkload.
type MigratableWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MigratableWorkload `json:"items"`
}

// EffectivePlacementLabel returns the placement label key/value with defaults applied.
func (m *MigratableWorkload) EffectivePlacementLabel() (string, string) {
	key, value := DefaultPlacementLabelKey, DefaultPlacementLabelValue
	if m.Spec.PlacementLabel != nil {
		if m.Spec.PlacementLabel.Key != "" {
			key = m.Spec.PlacementLabel.Key
		}
		if m.Spec.PlacementLabel.Value != "" {
			value = m.Spec.PlacementLabel.Value
		}
	}
	return key, value
}

// EffectiveCheckpointDir returns spec.checkpointDir or the default.
func (m *MigratableWorkload) EffectiveCheckpointDir() string {
	if m.Spec.CheckpointDir != "" {
		return m.Spec.CheckpointDir
	}
	return DefaultCheckpointDir
}

// ProcessMigrationEnabled reports whether DMTCP process-memory migration is
// enabled (default true).
func (m *MigratableWorkload) ProcessMigrationEnabled() bool {
	return m.Spec.ProcessMigration == nil || *m.Spec.ProcessMigration
}

// VolumeMigrationEnabled reports whether overlay volume migration is enabled
// (default true).
func (m *MigratableWorkload) VolumeMigrationEnabled() bool {
	return m.Spec.VolumeMigration == nil || *m.Spec.VolumeMigration
}

// EffectivePreSyncRounds returns the number of pre-downtime overlay sync
// rounds, honouring the VolumeMigration toggle.
func (m *MigratableWorkload) EffectivePreSyncRounds() int32 {
	if !m.VolumeMigrationEnabled() {
		return 0
	}
	if m.Spec.PreSyncRounds != nil {
		if *m.Spec.PreSyncRounds < 0 {
			return 0
		}
		return *m.Spec.PreSyncRounds
	}
	return DefaultPreSyncRounds
}

func init() {
	SchemeBuilder.Register(&MigratableWorkload{}, &MigratableWorkloadList{})
}
