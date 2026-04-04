package kub

import (
	"go-client/logs"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

var namespace = "mig-ready"

func GetPods(clientset *kubernetes.Clientset) int {
	pods, err := clientset.CoreV1().Pods(namespace).List(metav1.ListOptions{})
	if err != nil {
		logs.ErrorLogger.Println("Something went wrong", err.Error())
	}
	logs.InfoLogger.Println("There are ", len(pods.Items), " pods in the cluster\n")
	return len(pods.Items)
}

func GetPodsInterface(clientset kubernetes.Interface) int {
	pods, err := clientset.CoreV1().Pods(namespace).List(metav1.ListOptions{})
	if err != nil {
		logs.ErrorLogger.Println("Something went wrong", err.Error())
	}
	logs.InfoLogger.Println("There are", len(pods.Items), "pods in the cluster\n")
	return len(pods.Items)
}

func DeletePod(clientset *kubernetes.Clientset, podName string) bool {
	err := clientset.CoreV1().Pods(namespace).Delete(podName, &metav1.DeleteOptions{})
	if err != nil {
		logs.LogError(err)
		return false
	}
	return true
}

func ScaleUp(clientset *kubernetes.Clientset, deployName string) bool {
	s, err := clientset.AppsV1().Deployments(namespace).GetScale(deployName, metav1.GetOptions{})
	if err != nil {
		logs.LogError(err)
		return false
	}

	sc := *s
	sc.Spec.Replicas++

	_, err = clientset.AppsV1().Deployments(namespace).UpdateScale(deployName, &sc)
	if err != nil {
		logs.LogError(err)
		return false
	}
	return true
}

func ScaleDown(clientset *kubernetes.Clientset, deployName string) bool {
	s, err := clientset.AppsV1().Deployments(namespace).GetScale(deployName, metav1.GetOptions{})
	if err != nil {
		logs.LogError(err)
		return false
	}

	sc := *s
	sc.Spec.Replicas--

	_, err = clientset.AppsV1().Deployments(namespace).UpdateScale(deployName, &sc)
	if err != nil {
		logs.LogError(err)
		return false
	}
	return true
}

// AddNodeLabels applies the given key/value pairs as node labels and persists
// the change via the Kubernetes API.
func AddNodeLabels(clientset *kubernetes.Clientset, node *v1.Node, labels map[string]string) bool {
	for k, v := range labels {
		node.Labels[k] = v
	}
	_, err := clientset.CoreV1().Nodes().Update(node)
	if err != nil {
		logs.LogError(err)
		return false
	}
	return true
}

func FindNode(clientset *kubernetes.Clientset, nodeName string) *v1.Node {
	node, err := clientset.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})

	if err != nil {
		logs.ErrorLogger.Println("Something went wrong", err.Error())
	}

	return node
}
