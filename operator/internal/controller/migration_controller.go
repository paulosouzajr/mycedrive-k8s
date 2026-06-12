package controller

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/history"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
)

const (
	migrationFinalizer = "mycedrive.io/migration-cleanup"

	// requeueInterval drives the polling loop while a migration is active:
	// REST events land in the registry and the next reconcile pass picks
	// them up.
	requeueInterval = 3 * time.Second

	// phaseTimeout fails a migration stuck in one phase for too long.
	phaseTimeout = 10 * time.Minute
)

// MigrationReconciler reconciles Migration objects, driving the
// checkpoint → transfer → restore flow against the Execution Agents through
// the shared registry.
type MigrationReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry *registry.Registry
	// History is the optional metrics module; nil when not wired.
	History *history.Store
}

// +kubebuilder:rbac:groups=mycedrive.io,resources=migrations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mycedrive.io,resources=migrations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=mycedrive.io,resources=migrations/finalizers,verbs=update
// +kubebuilder:rbac:groups=mycedrive.io,resources=migratableworkloads,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch;update;patch

// Reconcile advances a Migration through its phases.
func (r *MigrationReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	mig := &mycedrivev1alpha1.Migration{}
	if err := r.Get(ctx, req.NamespacedName, mig); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}

	if !mig.DeletionTimestamp.IsZero() {
		return r.finalize(ctx, mig)
	}
	if mig.Status.IsTerminal() {
		return ctrl.Result{}, nil
	}

	if !controllerutil.ContainsFinalizer(mig, migrationFinalizer) {
		controllerutil.AddFinalizer(mig, migrationFinalizer)
		if err := r.Update(ctx, mig); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	mw := &mycedrivev1alpha1.MigratableWorkload{}
	if err := r.Get(ctx, types.NamespacedName{Namespace: mig.Namespace, Name: mig.Spec.WorkloadName}, mw); err != nil {
		if apierrors.IsNotFound(err) {
			return r.fail(ctx, mig, fmt.Sprintf("MigratableWorkload %q not found in namespace %q", mig.Spec.WorkloadName, mig.Namespace))
		}
		return ctrl.Result{}, err
	}

	if expired, msg := r.phaseExpired(mig); expired {
		return r.fail(ctx, mig, msg)
	}

	var (
		res ctrl.Result
		err error
	)
	switch mig.Status.Phase {
	case "":
		res, err = r.initialize(ctx, mig)
	case mycedrivev1alpha1.MigrationPhasePending:
		res, err = r.reconcilePending(ctx, mig, mw)
	case mycedrivev1alpha1.MigrationPhaseSyncing:
		res, err = r.reconcileSyncing(ctx, mig, mw)
	case mycedrivev1alpha1.MigrationPhaseCheckpointing:
		res, err = r.reconcileCheckpointing(ctx, mig)
	case mycedrivev1alpha1.MigrationPhaseTransferring:
		res, err = r.reconcileTransferring(ctx, mig, mw)
	case mycedrivev1alpha1.MigrationPhaseRestoring:
		res, err = r.reconcileRestoring(ctx, mig, mw)
	default:
		log.Info("unknown migration phase", "phase", mig.Status.Phase)
		return ctrl.Result{}, nil
	}
	if apierrors.IsConflict(err) {
		return ctrl.Result{RequeueAfter: time.Second}, nil
	}
	return res, err
}

// initialize validates the request and moves the migration to Pending.
func (r *MigrationReconciler) initialize(ctx context.Context, mig *mycedrivev1alpha1.Migration) (ctrl.Result, error) {
	for _, nodeName := range []string{mig.Spec.SourceNode, mig.Spec.TargetNode} {
		var node corev1.Node
		if err := r.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
			if apierrors.IsNotFound(err) {
				return r.fail(ctx, mig, fmt.Sprintf("node %q not found", nodeName))
			}
			return ctrl.Result{}, err
		}
	}
	if mig.Spec.SourceNode == mig.Spec.TargetNode {
		return r.fail(ctx, mig, "sourceNode and targetNode must differ")
	}

	now := metav1.Now()
	mig.Status.StartTime = &now
	return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhasePending, "preparing destination placement")
}

// reconcilePending labels nodes, resolves the source pod and prepares the
// destination (Deployments: scale-up), then hands off to pre-downtime sync
// or directly to the checkpoint flow.
func (r *MigrationReconciler) reconcilePending(ctx context.Context, mig *mycedrivev1alpha1.Migration, mw *mycedrivev1alpha1.MigratableWorkload) (ctrl.Result, error) {
	// 1. Resolve the source pod.
	if mig.Status.SourcePod == "" {
		pods, err := listWorkloadPods(ctx, r.Client, mw)
		if err != nil {
			return ctrl.Result{}, err
		}
		var source *corev1.Pod
		if mig.Spec.PodName != "" {
			for i := range pods {
				if pods[i].Name == mig.Spec.PodName {
					source = &pods[i]
					break
				}
			}
			if source == nil {
				return r.fail(ctx, mig, fmt.Sprintf("pod %q not found for workload %q", mig.Spec.PodName, mw.Name))
			}
			if source.Spec.NodeName != mig.Spec.SourceNode {
				return r.fail(ctx, mig, fmt.Sprintf("pod %q runs on node %q, not sourceNode %q", source.Name, source.Spec.NodeName, mig.Spec.SourceNode))
			}
		} else {
			source = findPodOnNode(pods, mig.Spec.SourceNode, "")
			if source == nil {
				return r.requeueWithMessage(ctx, mig, fmt.Sprintf("waiting for a pod of workload %q on node %q", mw.Name, mig.Spec.SourceNode))
			}
		}
		mig.Status.SourcePod = source.Name
		mig.Status.CheckpointDir = mw.EffectiveCheckpointDir()
		// Snapshot the mechanism toggles so later edits to the workload do
		// not change an in-flight migration.
		mig.Status.ProcessMigration = mw.ProcessMigrationEnabled()
		mig.Status.VolumeMigration = mw.VolumeMigrationEnabled()
		mig.Status.SyncRounds = mw.EffectivePreSyncRounds()
		if mw.Spec.WorkloadRef.Kind == mycedrivev1alpha1.WorkloadKindStatefulSet {
			// Stable names: the destination pod is the recreated source pod.
			mig.Status.DestinationPod = source.Name
		}
		if err := r.Status().Update(ctx, mig); err != nil {
			return ctrl.Result{}, err
		}
		return ctrl.Result{Requeue: true}, nil
	}

	// 2. Steer scheduling: placement label on target, removed from source.
	key, value := mw.EffectivePlacementLabel()
	if err := setNodeLabel(ctx, r.Client, mig.Spec.TargetNode, key, value); err != nil {
		return ctrl.Result{}, err
	}
	if err := removeNodeLabel(ctx, r.Client, mig.Spec.SourceNode, key); err != nil {
		return ctrl.Result{}, err
	}

	// 3. Arm the registry so /remove answers needsCheckpoint=true and the
	// EA learns which mechanisms (process/volume) are enabled.
	r.Registry.Arm(mig.Status.SourcePod, registry.ArmInfo{
		CheckpointDir:    mig.Status.CheckpointDir,
		ProcessMigration: mig.Status.ProcessMigration,
		VolumeMigration:  mig.Status.VolumeMigration,
		SyncRounds:       int(mig.Status.SyncRounds),
	})
	r.Registry.SetNode(mig.Status.SourcePod, mig.Spec.SourceNode)

	// 4. Deployments: create the destination replica before killing the
	// source, then gate on it being scheduled on the target node.
	if mw.Spec.WorkloadRef.Kind == mycedrivev1alpha1.WorkloadKindDeployment {
		if !mig.Status.ScaledUp {
			if err := scaleDeployment(ctx, r.Client, mw.Namespace, mw.Spec.WorkloadRef.Name, +1); err != nil {
				return ctrl.Result{}, err
			}
			mig.Status.ScaledUp = true
			if err := r.Status().Update(ctx, mig); err != nil {
				return ctrl.Result{}, err
			}
			return ctrl.Result{RequeueAfter: requeueInterval}, nil
		}
		if mig.Status.DestinationPod == "" {
			pods, err := listWorkloadPods(ctx, r.Client, mw)
			if err != nil {
				return ctrl.Result{}, err
			}
			dest := findPodOnNode(pods, mig.Spec.TargetNode, mig.Status.SourcePod)
			if dest == nil {
				return r.requeueWithMessage(ctx, mig, fmt.Sprintf("waiting for destination pod on node %q", mig.Spec.TargetNode))
			}
			mig.Status.DestinationPod = dest.Name
			if err := r.Status().Update(ctx, mig); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	// 5. Volume migration pre-copy: let the source EA transfer overlay
	// snapshot rounds while the pod is still running (CloudCom 2020) before
	// taking the downtime hit.
	if mig.Status.SyncRounds > 0 {
		return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseSyncing,
			fmt.Sprintf("pre-downtime overlay sync: waiting for %d round(s) from source Execution Agent", mig.Status.SyncRounds))
	}

	// 6. No pre-sync requested: enter downtime immediately.
	return r.deleteSourceAndAdvance(ctx, mig)
}

// reconcileSyncing waits for the source EA to complete the requested
// pre-downtime overlay snapshot rounds (reported via POST /sync), then
// deletes the source pod to enter the downtime window.
func (r *MigrationReconciler) reconcileSyncing(ctx context.Context, mig *mycedrivev1alpha1.Migration, _ *mycedrivev1alpha1.MigratableWorkload) (ctrl.Result, error) {
	rec, ok := r.Registry.Get(mig.Status.SourcePod)
	if !ok {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	if int32(rec.SyncRound) != mig.Status.SyncRound {
		mig.Status.SyncRound = int32(rec.SyncRound)
		if err := r.Status().Update(ctx, mig); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}
	if rec.SyncRound < rec.SyncRounds {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}
	return r.deleteSourceAndAdvance(ctx, mig)
}

// deleteSourceAndAdvance deletes the source pod (kubelet runs preStop → EA
// calls /remove → checkpoint work begins) and moves to the next phase:
// Checkpointing when any state mechanism is enabled, otherwise straight to
// Restoring (plain reschedule).
func (r *MigrationReconciler) deleteSourceAndAdvance(ctx context.Context, mig *mycedrivev1alpha1.Migration) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	var pod corev1.Pod
	err := r.Get(ctx, types.NamespacedName{Namespace: mig.Namespace, Name: mig.Status.SourcePod}, &pod)
	switch {
	case err == nil && pod.DeletionTimestamp == nil:
		if err := r.Delete(ctx, &pod); err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		log.Info("source pod deleted, downtime window started", "pod", mig.Status.SourcePod)
	case err != nil && !apierrors.IsNotFound(err):
		return ctrl.Result{}, err
	}

	if !mig.Status.ProcessMigration && !mig.Status.VolumeMigration {
		return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseRestoring,
			"no state mechanisms enabled; waiting for destination pod readiness")
	}
	return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseCheckpointing,
		"waiting for source Execution Agent to produce its final checkpoint")
}

// reconcileCheckpointing waits for the source EA to call POST /copy.
func (r *MigrationReconciler) reconcileCheckpointing(ctx context.Context, mig *mycedrivev1alpha1.Migration) (ctrl.Result, error) {
	rec, ok := r.Registry.Get(mig.Status.SourcePod)
	if ok && rec.CheckpointReady {
		return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseTransferring, "checkpoint written; transferring checkpoint and overlay layers to destination")
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// reconcileTransferring waits for the destination EA to register.
func (r *MigrationReconciler) reconcileTransferring(ctx context.Context, mig *mycedrivev1alpha1.Migration, mw *mycedrivev1alpha1.MigratableWorkload) (ctrl.Result, error) {
	destUp := false
	if mw.Spec.WorkloadRef.Kind == mycedrivev1alpha1.WorkloadKindStatefulSet {
		// Same pod name: destination re-registers under the source name.
		rec, ok := r.Registry.Get(mig.Status.SourcePod)
		destUp = ok && rec.DestRegistered
	} else {
		_, destUp = r.Registry.Get(mig.Status.DestinationPod)
	}
	if destUp {
		r.Registry.SetNode(mig.Status.DestinationPod, mig.Spec.TargetNode)
		return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseRestoring, "destination Execution Agent registered; restoring from checkpoint")
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// reconcileRestoring waits for POST /restored from the destination EA, or for
// the destination pod to report Ready, then completes the migration.
func (r *MigrationReconciler) reconcileRestoring(ctx context.Context, mig *mycedrivev1alpha1.Migration, mw *mycedrivev1alpha1.MigratableWorkload) (ctrl.Result, error) {
	restored := false
	if rec, ok := r.Registry.Get(mig.Status.DestinationPod); ok && rec.Restored {
		restored = true
	}
	if !restored {
		var pod corev1.Pod
		err := r.Get(ctx, types.NamespacedName{Namespace: mig.Namespace, Name: mig.Status.DestinationPod}, &pod)
		if err == nil && pod.Spec.NodeName == mig.Spec.TargetNode && isPodReady(&pod) {
			restored = true
		} else if err != nil && !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
	}
	if !restored {
		return ctrl.Result{RequeueAfter: requeueInterval}, nil
	}

	// Compensating scale-down for Deployments (the source replica is gone).
	if mig.Status.ScaledUp {
		if err := scaleDeployment(ctx, r.Client, mw.Namespace, mw.Spec.WorkloadRef.Name, -1); err != nil {
			return ctrl.Result{}, err
		}
		mig.Status.ScaledUp = false
	}

	r.clearRegistryFlags(mig)
	now := metav1.Now()
	mig.Status.CompletionTime = &now
	return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseCompleted, fmt.Sprintf("pod %s restored on node %s", mig.Status.DestinationPod, mig.Spec.TargetNode))
}

// finalize cleans registry state when a Migration is deleted mid-flight.
func (r *MigrationReconciler) finalize(ctx context.Context, mig *mycedrivev1alpha1.Migration) (ctrl.Result, error) {
	if controllerutil.ContainsFinalizer(mig, migrationFinalizer) {
		if !mig.Status.IsTerminal() {
			r.clearRegistryFlags(mig)
		}
		controllerutil.RemoveFinalizer(mig, migrationFinalizer)
		if err := r.Update(ctx, mig); err != nil {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{}, nil
}

func (r *MigrationReconciler) clearRegistryFlags(mig *mycedrivev1alpha1.Migration) {
	if mig.Status.SourcePod != "" {
		r.Registry.Disarm(mig.Status.SourcePod)
	}
	if mig.Status.DestinationPod != "" && mig.Status.DestinationPod != mig.Status.SourcePod {
		r.Registry.Disarm(mig.Status.DestinationPod)
	}
}

// setPhase records a phase transition and persists status.
func (r *MigrationReconciler) setPhase(ctx context.Context, mig *mycedrivev1alpha1.Migration, phase mycedrivev1alpha1.MigrationPhase, message string) (ctrl.Result, error) {
	now := metav1.Now()
	mig.Status.Phase = phase
	mig.Status.Message = message
	mig.Status.LastTransitionTime = &now
	if err := r.Status().Update(ctx, mig); err != nil {
		return ctrl.Result{}, err
	}
	r.recordHistory(mig, now.Time)
	if mig.Status.IsTerminal() {
		return ctrl.Result{}, nil
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// recordHistory forwards a persisted phase transition to the metrics module.
func (r *MigrationReconciler) recordHistory(mig *mycedrivev1alpha1.Migration, at time.Time) {
	if r.History == nil {
		return
	}
	r.History.RecordTransition(history.Transition{
		Namespace:        mig.Namespace,
		Name:             mig.Name,
		Workload:         mig.Spec.WorkloadName,
		SourcePod:        mig.Status.SourcePod,
		DestinationPod:   mig.Status.DestinationPod,
		SourceNode:       mig.Spec.SourceNode,
		TargetNode:       mig.Spec.TargetNode,
		ProcessMigration: mig.Status.ProcessMigration,
		VolumeMigration:  mig.Status.VolumeMigration,
		Phase:            string(mig.Status.Phase),
		Message:          mig.Status.Message,
		Time:             at,
	})
}

// SeedHistory rebuilds coarse history records (phase, start/completion — no
// per-step detail) from existing Migration CRs. Called once at startup with
// an uncached reader so the metrics module survives operator restarts.
func SeedHistory(ctx context.Context, reader client.Reader, store *history.Store) error {
	var list mycedrivev1alpha1.MigrationList
	if err := reader.List(ctx, &list); err != nil {
		return err
	}
	records := make([]history.Record, 0, len(list.Items))
	for i := range list.Items {
		mig := &list.Items[i]
		rec := history.Record{
			Name:             mig.Name,
			Namespace:        mig.Namespace,
			Workload:         mig.Spec.WorkloadName,
			SourcePod:        mig.Status.SourcePod,
			DestinationPod:   mig.Status.DestinationPod,
			SourceNode:       mig.Spec.SourceNode,
			TargetNode:       mig.Spec.TargetNode,
			ProcessMigration: mig.Status.ProcessMigration,
			VolumeMigration:  mig.Status.VolumeMigration,
			Phase:            string(mig.Status.Phase),
			Message:          mig.Status.Message,
		}
		if rec.Phase == "" {
			rec.Phase = string(mycedrivev1alpha1.MigrationPhasePending)
		}
		if mig.Status.StartTime != nil {
			rec.StartedAt = mig.Status.StartTime.Time
		} else {
			rec.StartedAt = mig.CreationTimestamp.Time
		}
		if mig.Status.CompletionTime != nil {
			t := mig.Status.CompletionTime.Time
			rec.CompletedAt = &t
		}
		records = append(records, rec)
	}
	store.Seed(records)
	return nil
}

// fail moves the migration to the terminal Failed phase.
func (r *MigrationReconciler) fail(ctx context.Context, mig *mycedrivev1alpha1.Migration, message string) (ctrl.Result, error) {
	logf.FromContext(ctx).Info("migration failed", "reason", message)
	r.clearRegistryFlags(mig)
	now := metav1.Now()
	mig.Status.CompletionTime = &now
	return r.setPhase(ctx, mig, mycedrivev1alpha1.MigrationPhaseFailed, message)
}

// requeueWithMessage updates status.message (best effort) and requeues.
func (r *MigrationReconciler) requeueWithMessage(ctx context.Context, mig *mycedrivev1alpha1.Migration, message string) (ctrl.Result, error) {
	if mig.Status.Message != message {
		mig.Status.Message = message
		if err := r.Status().Update(ctx, mig); err != nil && !apierrors.IsConflict(err) {
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: requeueInterval}, nil
}

// phaseExpired reports whether the current non-terminal phase exceeded
// phaseTimeout.
func (r *MigrationReconciler) phaseExpired(mig *mycedrivev1alpha1.Migration) (bool, string) {
	if mig.Status.Phase == "" || mig.Status.IsTerminal() || mig.Status.LastTransitionTime == nil {
		return false, ""
	}
	if time.Since(mig.Status.LastTransitionTime.Time) > phaseTimeout {
		return true, fmt.Sprintf("phase %s timed out after %s", mig.Status.Phase, phaseTimeout)
	}
	return false, ""
}

// SetupWithManager registers the controller with the manager.
func (r *MigrationReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mycedrivev1alpha1.Migration{}).
		Named("migration").
		Complete(r)
}
