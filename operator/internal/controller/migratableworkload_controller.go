package controller

import (
	"context"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
)

// mirrorInterval refreshes the registry mirror in CRD status even without
// spec changes, so registrations arriving over REST become visible.
const mirrorInterval = 15 * time.Second

// MigratableWorkloadReconciler validates the workload reference and mirrors
// the in-memory registry into status.registeredPods so registrations survive
// operator restarts.
type MigratableWorkloadReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Registry *registry.Registry
}

// +kubebuilder:rbac:groups=mycedrive.io,resources=migratableworkloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=mycedrive.io,resources=migratableworkloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=apps,resources=statefulsets;deployments,verbs=get;list;watch

// Reconcile validates the referenced workload and mirrors registry state.
func (r *MigratableWorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := logf.FromContext(ctx)

	mw := &mycedrivev1alpha1.MigratableWorkload{}
	if err := r.Get(ctx, req.NamespacedName, mw); err != nil {
		return ctrl.Result{}, client.IgnoreNotFound(err)
	}
	if !mw.DeletionTimestamp.IsZero() {
		return ctrl.Result{}, nil
	}

	phase, message := "Ready", ""
	exists, err := workloadExists(ctx, r.Client, mw)
	if err != nil {
		phase, message = "Pending", err.Error()
	} else if !exists {
		phase, message = "Pending", mw.Spec.WorkloadRef.Kind+" "+mw.Spec.WorkloadRef.Name+" not found"
	}

	// Mirror registry records belonging to this workload into status.
	var mirrored []mycedrivev1alpha1.RegisteredPod
	for _, rec := range r.Registry.List() {
		if !podBelongsToWorkload(rec.Name, mw.Spec.WorkloadRef.Name) {
			continue
		}
		if rec.WorkloadNamespace != "" && rec.WorkloadNamespace != mw.Namespace {
			continue
		}
		r.Registry.SetWorkload(rec.Name, mw.Namespace, mw.Name)
		registeredAt := metav1.NewTime(rec.RegisteredAt)
		mirrored = append(mirrored, mycedrivev1alpha1.RegisteredPod{
			Name:            rec.Name,
			Address:         rec.Address,
			ContainerPort:   int32(rec.ContainerPort),
			Node:            rec.Node,
			Migrating:       rec.Migrating,
			CheckpointReady: rec.CheckpointReady,
			CheckpointDir:   rec.CheckpointDir,
			Restored:        rec.Restored,
			RegisteredAt:    &registeredAt,
		})
	}

	updated := mw.DeepCopy()
	updated.Status.Phase = phase
	updated.Status.Message = message
	updated.Status.RegisteredPods = mirrored
	updated.Status.ObservedGeneration = mw.Generation

	if !equality.Semantic.DeepEqual(mw.Status, updated.Status) {
		if err := r.Status().Update(ctx, updated); err != nil {
			log.Error(err, "updating MigratableWorkload status")
			return ctrl.Result{}, err
		}
	}
	return ctrl.Result{RequeueAfter: mirrorInterval}, nil
}

// SeedRegistry restores the in-memory registry from MigratableWorkload
// statuses. Called once at startup with an uncached reader.
func SeedRegistry(ctx context.Context, reader client.Reader, reg *registry.Registry) error {
	var list mycedrivev1alpha1.MigratableWorkloadList
	if err := reader.List(ctx, &list); err != nil {
		return err
	}
	var records []registry.PodRecord
	for i := range list.Items {
		mw := &list.Items[i]
		for _, p := range mw.Status.RegisteredPods {
			rec := registry.PodRecord{
				Name:              p.Name,
				Address:           p.Address,
				ContainerPort:     int(p.ContainerPort),
				Node:              p.Node,
				WorkloadNamespace: mw.Namespace,
				WorkloadName:      mw.Name,
				Migrating:         p.Migrating,
				CheckpointReady:   p.CheckpointReady,
				CheckpointDir:     p.CheckpointDir,
				Restored:          p.Restored,
				Registrations:     1,
				LastSeen:          time.Now(),
			}
			if p.RegisteredAt != nil {
				rec.RegisteredAt = p.RegisteredAt.Time
			} else {
				rec.RegisteredAt = time.Now()
			}
			records = append(records, rec)
		}
	}
	reg.Seed(records)
	return nil
}

// SetupWithManager registers the controller with the manager.
func (r *MigratableWorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&mycedrivev1alpha1.MigratableWorkload{}).
		Named("migratableworkload").
		Complete(r)
}
