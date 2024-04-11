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

	logs.ErrorLogger.Println(err)

	if err != nil {
		return true
	} else {
		return false
	}
}

func ScaleUp(clientset *kubernetes.Clientset, deployName string) bool {
	s, err := clientset.AppsV1().Deployments(namespace).GetScale(deployName, metav1.GetOptions{})
	logs.ErrorLogger.Println(err)

	sc := *s
	sc.Spec.Replicas = sc.Spec.Replicas + 1

	_, err = clientset.AppsV1().
		Deployments(namespace).
		UpdateScale(deployName, &sc)

	logs.ErrorLogger.Println(err)

	if err != nil {
		return true
	} else {
		return false
	}
}

func ScaleDown(clientset *kubernetes.Clientset, deployName string) bool {
	s, err := clientset.AppsV1().Deployments(namespace).GetScale(deployName, metav1.GetOptions{})
	logs.ErrorLogger.Println(err)

	sc := *s
	sc.Spec.Replicas = sc.Spec.Replicas - 1

	_, err = clientset.AppsV1().
		Deployments(namespace).
		UpdateScale(deployName, &sc)

	logs.ErrorLogger.Println(err)

	if err != nil {
		return true
	} else {
		return false
	}
}

// AddNodeLabels adds labels to node
func AddNodeLabels(node *v1.Node, labels map[string]string) {
	for name, value := range labels {
		node.Labels[namespace+name] = value
		node.Annotations[namespace+"-"+namespace+name] = value
	}
}

func FindNode(clientset *kubernetes.Clientset, nodeName string) *v1.Node {
	node, err := clientset.CoreV1().Nodes().Get(nodeName, metav1.GetOptions{})

	if err != nil {
		logs.ErrorLogger.Println("Something went wrong", err.Error())
	}

	return node
}
