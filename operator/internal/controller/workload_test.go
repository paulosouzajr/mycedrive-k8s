package controller

import (
	"context"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
)

func TestListWorkloadPodsUsesWorkloadSelector(t *testing.T) {
	scheme := controllerTestScheme(t)
	mw := &mycedrivev1alpha1.MigratableWorkload{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "test"},
		Spec: mycedrivev1alpha1.MigratableWorkloadSpec{
			WorkloadRef: mycedrivev1alpha1.WorkloadReference{Kind: mycedrivev1alpha1.WorkloadKindStatefulSet, Name: "web"},
		},
	}
	workload := &appsv1.StatefulSet{
		ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "test"},
		Spec:       appsv1.StatefulSetSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "web"}}},
	}
	matching := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-0", Namespace: "test", Labels: map[string]string{"app": "web"}}}
	// The old name-prefix matcher incorrectly included this unrelated
	// workload. A canary or similarly named Deployment must not be migrated.
	unrelated := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "web-canary-0", Namespace: "test", Labels: map[string]string{"app": "web-canary"}}}
	kubeClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(workload, matching, unrelated).Build()

	pods, err := listWorkloadPods(context.Background(), kubeClient, mw)
	if err != nil {
		t.Fatal(err)
	}
	if len(pods) != 1 || pods[0].Name != matching.Name {
		t.Fatalf("selected pods = %#v, want only %s", pods, matching.Name)
	}
}
