package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	mycedrivev1alpha1 "github.com/paulosouzajr/mycedrive-k8s/operator/api/v1alpha1"
)

// podBelongsToWorkload matches pods to a workload by name convention:
// StatefulSet pods are <name>-<ordinal>, Deployment pods are
// <name>-<rs-hash>-<pod-hash>. A prefix match on "<name>-" covers both.
func podBelongsToWorkload(podName, workloadName string) bool {
	return strings.HasPrefix(podName, workloadName+"-")
}

// listWorkloadPods returns the pods selected by the referenced workload's
// pod selector. Names are not a reliable ownership boundary: a StatefulSet
// named "web" may coexist with a Deployment named "web-canary", whose pod
// names have the same prefix.
func listWorkloadPods(ctx context.Context, c client.Client, mw *mycedrivev1alpha1.MigratableWorkload) ([]corev1.Pod, error) {
	selector, err := workloadPodSelector(ctx, c, mw)
	if err != nil {
		return nil, err
	}
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(mw.Namespace), client.MatchingLabelsSelector{Selector: selector}); err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", mw.Namespace, err)
	}
	return list.Items, nil
}

// workloadPodSelector resolves the exact label selector from the referenced
// workload. It is deliberately shared by neither status mirroring nor the
// REST API because those paths only have an agent pod name, not a pod object.
func workloadPodSelector(ctx context.Context, c client.Client, mw *mycedrivev1alpha1.MigratableWorkload) (labels.Selector, error) {
	key := types.NamespacedName{Namespace: mw.Namespace, Name: mw.Spec.WorkloadRef.Name}
	var raw *metav1.LabelSelector
	switch mw.Spec.WorkloadRef.Kind {
	case mycedrivev1alpha1.WorkloadKindStatefulSet:
		var sts appsv1.StatefulSet
		if err := c.Get(ctx, key, &sts); err != nil {
			return nil, fmt.Errorf("get StatefulSet %s/%s: %w", key.Namespace, key.Name, err)
		}
		raw = sts.Spec.Selector
	case mycedrivev1alpha1.WorkloadKindDeployment:
		var dep appsv1.Deployment
		if err := c.Get(ctx, key, &dep); err != nil {
			return nil, fmt.Errorf("get Deployment %s/%s: %w", key.Namespace, key.Name, err)
		}
		raw = dep.Spec.Selector
	default:
		return nil, fmt.Errorf("unsupported workload kind %q", mw.Spec.WorkloadRef.Kind)
	}
	if raw == nil {
		return nil, fmt.Errorf("workload %s/%s has no pod selector", key.Namespace, key.Name)
	}
	selector, err := metav1.LabelSelectorAsSelector(raw)
	if err != nil {
		return nil, fmt.Errorf("parse pod selector for %s/%s: %w", key.Namespace, key.Name, err)
	}
	return selector, nil
}

// findPodOnNode returns the newest pod scheduled on node, excluding
// excludeName. Returns nil when no pod matches.
func findPodOnNode(pods []corev1.Pod, node, excludeName string) *corev1.Pod {
	var best *corev1.Pod
	for i := range pods {
		p := &pods[i]
		if p.Spec.NodeName != node || p.Name == excludeName {
			continue
		}
		if p.DeletionTimestamp != nil {
			continue
		}
		if best == nil || p.CreationTimestamp.After(best.CreationTimestamp.Time) {
			best = p
		}
	}
	return best
}

// isPodReady reports whether the pod's Ready condition is True.
func isPodReady(pod *corev1.Pod) bool {
	for _, cond := range pod.Status.Conditions {
		if cond.Type == corev1.PodReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

// setNodeLabel ensures the node carries label key=value.
func setNodeLabel(ctx context.Context, c client.Client, nodeName, key, value string) error {
	var node corev1.Node
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("get node %s: %w", nodeName, err)
	}
	if node.Labels[key] == value {
		return nil
	}
	patch := client.MergeFrom(node.DeepCopy())
	if node.Labels == nil {
		node.Labels = map[string]string{}
	}
	node.Labels[key] = value
	return c.Patch(ctx, &node, patch)
}

// removeNodeLabel ensures the node does not carry label key. The label is
// removed entirely (not set to "") so node selectors stop matching cleanly.
func removeNodeLabel(ctx context.Context, c client.Client, nodeName, key string) error {
	var node corev1.Node
	if err := c.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
		return fmt.Errorf("get node %s: %w", nodeName, err)
	}
	if _, ok := node.Labels[key]; !ok {
		return nil
	}
	patch := client.MergeFrom(node.DeepCopy())
	delete(node.Labels, key)
	return c.Patch(ctx, &node, patch)
}

// scaleDeployment adjusts the replica count of a Deployment by delta
// (StatefulSets keep their replica count: same-name pod recreation drives
// their migration flow instead).
func scaleDeployment(ctx context.Context, c client.Client, namespace, name string, delta int32) error {
	var dep appsv1.Deployment
	if err := c.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, &dep); err != nil {
		return fmt.Errorf("get deployment %s/%s: %w", namespace, name, err)
	}
	replicas := int32(1)
	if dep.Spec.Replicas != nil {
		replicas = *dep.Spec.Replicas
	}
	replicas += delta
	if replicas < 0 {
		replicas = 0
	}
	patch := client.MergeFrom(dep.DeepCopy())
	dep.Spec.Replicas = &replicas
	return c.Patch(ctx, &dep, patch)
}

// workloadExists verifies the referenced StatefulSet or Deployment is present.
func workloadExists(ctx context.Context, c client.Client, mw *mycedrivev1alpha1.MigratableWorkload) (bool, error) {
	key := types.NamespacedName{Namespace: mw.Namespace, Name: mw.Spec.WorkloadRef.Name}
	switch mw.Spec.WorkloadRef.Kind {
	case mycedrivev1alpha1.WorkloadKindStatefulSet:
		var sts appsv1.StatefulSet
		err := c.Get(ctx, key, &sts)
		return err == nil, client.IgnoreNotFound(err)
	case mycedrivev1alpha1.WorkloadKindDeployment:
		var dep appsv1.Deployment
		err := c.Get(ctx, key, &dep)
		return err == nil, client.IgnoreNotFound(err)
	default:
		return false, fmt.Errorf("unsupported workload kind %q", mw.Spec.WorkloadRef.Kind)
	}
}
