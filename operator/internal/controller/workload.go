package controller

import (
	"context"
	"fmt"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
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

// listWorkloadPods returns the pods of the referenced workload, matched by
// namespace and name prefix.
func listWorkloadPods(ctx context.Context, c client.Client, mw *mycedrivev1alpha1.MigratableWorkload) ([]corev1.Pod, error) {
	var list corev1.PodList
	if err := c.List(ctx, &list, client.InNamespace(mw.Namespace)); err != nil {
		return nil, fmt.Errorf("list pods in %s: %w", mw.Namespace, err)
	}
	var out []corev1.Pod
	for i := range list.Items {
		if podBelongsToWorkload(list.Items[i].Name, mw.Spec.WorkloadRef.Name) {
			out = append(out, list.Items[i])
		}
	}
	return out, nil
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
