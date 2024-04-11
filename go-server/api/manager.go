package api

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"go-client/kub"
	"go-client/logs"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"net/http"
	"strings"
)

//type PodManager struct {
//	Clients    map[*Pod]bool
//	Register   chan *Pod
//	Unregister chan *Pod
//}

var (
	err       error
	inter     kubernetes.Interface
	clientset *kubernetes.Clientset
)

type Pod struct {
	// socket   net.Conn
	mig        bool
	metaData   string
	podName    string
	podAddress string
}

type Message struct {
	podName    string
	podAddress string
	isNew      bool
	isMig      bool
}

var pods []Pod

func RegisterPod(c *gin.Context) {
	var message Message

	// Call BindJSON to bind the received JSON
	if err := c.BindJSON(&message); err != nil {
		return
	}

	for _, a := range pods {
		if a.podName == message.podName {
			c.IndentedJSON(http.StatusOK, Message{podAddress: message.podAddress, isNew: false,
				podName: message.podName, isMig: a.mig})
			return
		}
	}
	newPod := Pod{podName: message.podName, podAddress: message.podAddress, mig: false, metaData: ""}
	// Add the new album to the slice.
	pods = append(pods, newPod)
	c.IndentedJSON(http.StatusCreated, newPod)
}

func MigratePod(c *gin.Context) {
	podName := c.Param("deployment")
	origin := c.Param("originNode")
	dest := c.Param("destNode")

	inter, err = expK8sClient("http://192.168.1.118:8080", "../minikube/config")

	logs.LogError(err)

	kub.GetPodsInterface(inter)
	kub.GetPods(clientset)

	config, err := rest.InClusterConfig()
	logs.LogError(err)

	podExist := false

	for _, a := range pods {
		if a.podName == podName {
			a.mig, podExist = true, true
		}
	}

	if !podExist {
		c.IndentedJSON(http.StatusNotFound, "Pod doesn't exist")
		return
	}

	print(config)
	migrate(config, podName, "mig-ready", origin, dest)

	c.IndentedJSON(http.StatusOK, true)

}

func migrate(config *rest.Config, deployment string, label string, currentNode string, destNode string) {
	if containsEmpty(deployment, label, currentNode, destNode) {
		logs.LogError(fmt.Errorf("Missing parameters: %s %s %s %s\n", deployment, label, currentNode, destNode))
	}

	// creates the clientset
	clientset, err = kubernetes.NewForConfig(config)

	cNode := kub.FindNode(clientset, currentNode)
	dNode := kub.FindNode(clientset, destNode)

	//Validate the labels format
	if len(strings.Split(label, ":")) != 2 {
		logs.LogError(fmt.Errorf("incorrect label format. Use: label:content"))
	}

	// Remove labels from the older deployment
	kub.AddNodeLabels(cNode, map[string]string{
		strings.Split(label, ":")[0]: ""})

	// Add new labels to the desired node
	kub.AddNodeLabels(dNode, map[string]string{
		strings.Split(label, ":")[0]: strings.Split(label, ":")[1]})

	// Increase the amount of pods of that deployment to two
	if kub.ScaleUp(clientset, deployment) {
		logs.LogInfo("Scale up executed")
	} else {
		logs.LogError(fmt.Errorf("scale up error"))
	}

	// Request termination of the old pod
	if kub.DeletePod(clientset, deployment) {
		logs.LogInfo("Delete pod executed")
	} else {
		logs.LogError(fmt.Errorf("delete pod error"))
	}

}

func expK8sClient(masterUrl, kubeconfigPath string) (kubernetes.Interface, error) {
	// use the current context in kubeconfig
	config, err := clientcmd.BuildConfigFromFlags(masterUrl, kubeconfigPath)
	if err != nil {
		return nil, err
	}

	// create the clientset
	return kubernetes.NewForConfig(config)
}

func containsEmpty(ss ...string) bool {
	for _, s := range ss {
		if s == "" {
			return true
		}
	}
	return false
}

//func StartServer(manager PodManager) {
//	logs.LogInfo("Starting server...")
//	listener, error := net.Listen("tcp", ":3333")
//	if error != nil {
//		fmt.Println(error)
//	}
//	go manager.start()
//	for {
//		connection, _ := listener.Accept()
//		if error != nil {
//			logs.LogError(error)
//		}
//		client := &Pod{socket: connection, data: make(chan []byte)}
//		manager.Register <- client
//		go manager.receive(client)
//		go manager.send(client)
//	}
//}

//func (manager *PodManager) start() {
//	for {
//		select {
//		case connection := <-manager.Register:
//			manager.Clients[connection] = true
//			logs.LogInfo("Registering new pod")
//		case connection := <-manager.Unregister:
//			if _, ok := manager.Clients[connection]; ok {
//				close(connection.data)
//				delete(manager.Clients, connection)
//				logs.LogInfo("A connection has terminated, removing pod!")
//			}
//		}
//	}
//}
//
//func (manager *PodManager) send(client *Pod) {
//	defer client.socket.Close()
//	for {
//		select {
//		case message, ok := <-client.data:
//			if !ok {
//				return
//			}
//			client.socket.Write(message)
//		}
//	}
//}
//
//func (manager *PodManager) receive(client *Pod) {
//	for {
//		message := make([]byte, 4096)
//		length, err := client.socket.Read(message)
//		if err != nil {
//			manager.Unregister <- client
//			client.socket.Close()
//			break
//		}
//		if length > 0 {
//			client.metaData = string(message[:])
//			logs.LogInfo("Received Data: " + strings.Replace(client.metaData, ";", "\n", -2))
//		}
//	}
//}
