package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// MigrationPhase describes where a Migration is in its lifecycle.
type MigrationPhase string

const (
	// MigrationPhasePending: nodes are being labelled, the destination is
	// being prepared (Deployments: scale-up) and the source pod identified.
	MigrationPhasePending MigrationPhase = "Pending"
	// MigrationPhaseSyncing: iterative overlay snapshot rounds are being
	// transferred while the source pod is still running (volume migration
	// pre-copy, CloudCom 2020); the source EA reports each round via
	// POST /sync. Skipped when volume migration is disabled or
	// preSyncRounds is 0.
	MigrationPhaseSyncing MigrationPhase = "Syncing"
	// MigrationPhaseCheckpointing: the source pod has been deleted; its
	// preStop hook is producing a DMTCP checkpoint (EA answered
	// needsCheckpoint=true on POST /remove; phase advances on POST /copy).
	MigrationPhaseCheckpointing MigrationPhase = "Checkpointing"
	// MigrationPhaseTransferring: checkpoint and overlay layers are moving
	// to the destination; phase advances when the destination Execution
	// Agent registers (POST /register answered with isMig=true).
	MigrationPhaseTransferring MigrationPhase = "Transferring"
	// MigrationPhaseRestoring: the destination EA is restarting the process
	// from the checkpoint; phase advances on POST /restored or when the
	// destination pod reports Ready.
	MigrationPhaseRestoring MigrationPhase = "Restoring"
	// MigrationPhaseCompleted: terminal success.
	MigrationPhaseCompleted MigrationPhase = "Completed"
	// MigrationPhaseFailed: terminal failure; see status.message.
	MigrationPhaseFailed MigrationPhase = "Failed"
)

// MigrationSpec is a request to move one pod of a MigratableWorkload from
// sourceNode to targetNode.
type MigrationSpec struct {
	// WorkloadName is the name of a MigratableWorkload in the same namespace.
	WorkloadName string `json:"workloadName"`

	// PodName optionally selects which pod of the workload to migrate.
	// When empty the operator picks the workload's pod running on sourceNode.
	// +optional
	PodName string `json:"podName,omitempty"`

	// SourceNode is the node the pod currently runs on.
	SourceNode string `json:"sourceNode"`

	// TargetNode is the node the pod must be moved to.
	TargetNode string `json:"targetNode"`
}

// MigrationStatus is the observed state of a Migration.
type MigrationStatus struct {
	// +optional
	Phase MigrationPhase `json:"phase,omitempty"`
	// Message is a human-readable explanation of the current state.
	// +optional
	Message string `json:"message,omitempty"`
	// SourcePod is the resolved pod being checkpointed.
	// +optional
	SourcePod string `json:"sourcePod,omitempty"`
	// DestinationPod is the pod restoring on the target node. For
	// StatefulSets this equals SourcePod (stable pod names).
	// +optional
	DestinationPod string `json:"destinationPod,omitempty"`
	// CheckpointDir is the checkpoint directory used for this migration.
	// +optional
	CheckpointDir string `json:"checkpointDir,omitempty"`
	// ProcessMigration is the resolved DMTCP toggle copied from the
	// MigratableWorkload when the migration started.
	// +optional
	ProcessMigration bool `json:"processMigration,omitempty"`
	// VolumeMigration is the resolved overlay toggle copied from the
	// MigratableWorkload when the migration started.
	// +optional
	VolumeMigration bool `json:"volumeMigration,omitempty"`
	// SyncRounds is the number of pre-downtime overlay rounds requested.
	// +optional
	SyncRounds int32 `json:"syncRounds,omitempty"`
	// SyncRound is the last pre-downtime overlay round completed by the
	// source Execution Agent.
	// +optional
	SyncRound int32 `json:"syncRound,omitempty"`
	// ScaledUp records that the operator scaled a Deployment up and still
	// owes a compensating scale-down on completion.
	// +optional
	ScaledUp bool `json:"scaledUp,omitempty"`
	// +optional
	StartTime *metav1.Time `json:"startTime,omitempty"`
	// +optional
	CompletionTime *metav1.Time `json:"completionTime,omitempty"`
	// LastTransitionTime is when the phase last changed (used for timeouts).
	// +optional
	LastTransitionTime *metav1.Time `json:"lastTransitionTime,omitempty"`
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// IsTerminal reports whether the migration reached a terminal phase.
func (s *MigrationStatus) IsTerminal() bool {
	return s.Phase == MigrationPhaseCompleted || s.Phase == MigrationPhaseFailed
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=mig
// +kubebuilder:printcolumn:name="Workload",type=string,JSONPath=`.spec.workloadName`
// +kubebuilder:printcolumn:name="Pod",type=string,JSONPath=`.status.sourcePod`
// +kubebuilder:printcolumn:name="Source",type=string,JSONPath=`.spec.sourceNode`
// +kubebuilder:printcolumn:name="Target",type=string,JSONPath=`.spec.targetNode`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// Migration represents one stateful pod migration request.
type Migration struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MigrationSpec   `json:"spec,omitempty"`
	Status MigrationStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MigrationList contains a list of Migration.
type MigrationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Migration `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Migration{}, &MigrationList{})
}
