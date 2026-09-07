package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
	"github.com/paulosouzajr/mycedrive-k8s/operator/pkg/registry"
)

func controllerTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{
		corev1.AddToScheme,
		appsv1.AddToScheme,
		mycedrivev1alpha1.AddToScheme,
	} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	return scheme
}

func getMigration(t *testing.T, c client.Client, key types.NamespacedName) *mycedrivev1alpha1.Migration {
	t.Helper()
	mig := &mycedrivev1alpha1.Migration{}
	if err := c.Get(context.Background(), key, mig); err != nil {
		t.Fatalf("get Migration: %v", err)
	}
	return mig
}

func reconcileMigration(t *testing.T, r *MigrationReconciler, key types.NamespacedName) *mycedrivev1alpha1.Migration {
	t.Helper()
	if _, err := r.Reconcile(context.Background(), ctrl.Request{NamespacedName: key}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	return getMigration(t, r.Client, key)
}

// TestStatefulSetMigrationLifecycle exercises every controller transition
// that is driven by Kubernetes state. The agent notifications themselves are
// represented by the shared registry, exactly as the REST handlers do.
func TestStatefulSetMigrationLifecycle(t *testing.T) {
	const (
		namespace  = "test"
		workload   = "broker"
		podName    = "broker-0"
		sourceNode = "node-a"
		targetNode = "node-b"
	)
	key := types.NamespacedName{Namespace: namespace, Name: "move-broker"}

	zero := int32(0)
	mw := &mycedrivev1alpha1.MigratableWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: workload, Namespace: namespace},
		Spec: mycedrivev1alpha1.MigratableWorkloadSpec{
			WorkloadRef:   mycedrivev1alpha1.WorkloadReference{Kind: mycedrivev1alpha1.WorkloadKindStatefulSet, Name: workload},
			PreSyncRounds: &zero,
		},
	}
	sts := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: workload, Namespace: namespace},
		Spec:       appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": workload}}},
	}
	sourcePod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: podName, Namespace: namespace, Labels: map[string]string{"app": workload}},
		Spec:       corev1.PodSpec{NodeName: sourceNode},
	}
	mig := &mycedrivev1alpha1.Migration{
		ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace},
		Spec: mycedrivev1alpha1.MigrationSpec{
			WorkloadName: workload,
			PodName:      podName,
			SourceNode:   sourceNode,
			TargetNode:   targetNode,
		},
	}

	scheme := controllerTestScheme(t)
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).
		WithStatusSubresource(&mycedrivev1alpha1.Migration{}).
		WithObjects(
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: sourceNode, Labels: map[string]string{"mig-ready": "true"}}},
			&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: targetNode}},
			mw, sts, sourcePod, mig,
		).Build()
	reg := registry.New()
	reg.Register(podName, "10.0.0.5:2486", 2486)
	reconciler := &MigrationReconciler{Client: kubeClient, Scheme: scheme, Registry: reg}

	// First reconcile adds the finalizer, second validates the nodes and
	// initialises Pending, third resolves the source pod and snapshots config.
	reconcileMigration(t, reconciler, key)
	got := reconcileMigration(t, reconciler, key)
	if got.Status.Phase != mycedrivev1alpha1.MigrationPhasePending {
		t.Fatalf("phase after initialisation = %q, want Pending", got.Status.Phase)
	}
	got = reconcileMigration(t, reconciler, key)
	if got.Status.SourcePod != podName || got.Status.DestinationPod != podName {
		t.Fatalf("resolved pods = source %q, destination %q", got.Status.SourcePod, got.Status.DestinationPod)
	}

	// The controller steers scheduling and deletes the source. Default
	// pre-sync is deliberately zero, so this must advance directly rather
	// than waiting in Syncing for an unsupported agent protocol.
	got = reconcileMigration(t, reconciler, key)
	if got.Status.Phase != mycedrivev1alpha1.MigrationPhaseCheckpointing {
		t.Fatalf("phase after source deletion = %q, want Checkpointing", got.Status.Phase)
	}
	var source corev1.Node
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: sourceNode}, &source); err != nil {
		t.Fatal(err)
	}
	if _, found := source.Labels["mig-ready"]; found {
		t.Fatalf("source placement label was not removed: %v", source.Labels)
	}
	var target corev1.Node
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Name: targetNode}, &target); err != nil {
		t.Fatal(err)
	}
	if target.Labels["mig-ready"] != "true" {
		t.Fatalf("target placement label = %q, want true", target.Labels["mig-ready"])
	}
	var deleted corev1.Pod
	if err := kubeClient.Get(context.Background(), types.NamespacedName{Namespace: namespace, Name: podName}, &deleted); err == nil {
		t.Fatal("source pod was not deleted")
	}

	reg.MarkCheckpointReady(podName, "/dmtcp/checkpoints")
	got = reconcileMigration(t, reconciler, key)
	if got.Status.Phase != mycedrivev1alpha1.MigrationPhaseTransferring {
		t.Fatalf("phase after checkpoint = %q, want Transferring", got.Status.Phase)
	}

	// A duplicate registration is the replacement StatefulSet pod.
	reg.Register(podName, "10.0.1.5:2486", 2486)
	got = reconcileMigration(t, reconciler, key)
	if got.Status.Phase != mycedrivev1alpha1.MigrationPhaseRestoring {
		t.Fatalf("phase after destination registration = %q, want Restoring", got.Status.Phase)
	}

	reg.MarkRestored(podName)
	got = reconcileMigration(t, reconciler, key)
	if got.Status.Phase != mycedrivev1alpha1.MigrationPhaseCompleted {
		t.Fatalf("phase after restore = %q, want Completed", got.Status.Phase)
	}
	if got.Status.CompletionTime == nil {
		t.Fatal("completed migration has no completion time")
	}
	if rec, ok := reg.Get(podName); !ok || rec.Migrating {
		t.Fatalf("registry was not disarmed on completion: %+v", rec)
	}
}
