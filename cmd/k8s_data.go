package cmd

import (
	"context"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/tools/clientcmd/api"
)

// KubeClientWrapper wraps kubernetes clientset and configuration
type KubeClientWrapper struct {
	Clientset *kubernetes.Clientset
	Config    *api.Config
}

// NewKubeClient creates a new KubeClient with the current context
func NewKubeClient() (*KubeClientWrapper, string, error) {
	// Get current context name
	loadingRules := clientcmd.NewDefaultClientConfigLoadingRules()
	configOverrides := &clientcmd.ConfigOverrides{}
	kubeConfig := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(loadingRules, configOverrides)

	config, err := kubeConfig.ClientConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get client config: %v", err)
	}

	rawConfig, err := kubeConfig.RawConfig()
	if err != nil {
		return nil, "", fmt.Errorf("failed to get raw config: %v", err)
	}

	currentContext := rawConfig.CurrentContext
	contextInfo := rawConfig.Contexts[currentContext]
	clusterName := contextInfo.Cluster
	if clusterName == "" {
		clusterName = currentContext
	}

	// Create clientset
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create clientset: %v", err)
	}

	return &KubeClientWrapper{
		Clientset: clientset,
		Config:    &rawConfig,
	}, clusterName, nil
}

// K8sDataProvider defines the interface for accessing Kubernetes data
type K8sDataProvider interface {
	// GetClusterName returns the name of the cluster
	GetClusterName() string

	// UpdateNodeData fetches the latest node and pod data
	// Returns:
	// - map[string]NodeData: node data indexed by node name
	// - map[string]map[string][]string: pod indicators by node and namespace
	// - error: any error that occurred
	UpdateNodeData(includeNamespaces, excludeNamespaces map[string]bool) (map[string]NodeData, map[string]map[string][]string, error)

	// GetNodeMap returns the current node map
	GetNodeMap() map[string]*corev1.Node
}

// RealK8sDataProvider implements K8sDataProvider using actual Kubernetes cluster
type RealK8sDataProvider struct {
	BaseK8sDataProvider
	client      *KubeClientWrapper
	clusterName string
}

// NewRealK8sDataProvider creates a new RealK8sDataProvider
func NewRealK8sDataProvider() (*RealK8sDataProvider, error) {
	client, clusterName, err := NewKubeClient()
	if err != nil {
		return nil, err
	}

	return &RealK8sDataProvider{
		BaseK8sDataProvider: BaseK8sDataProvider{
			nodeMap: make(map[string]*corev1.Node),
		},
		client:      client,
		clusterName: clusterName,
	}, nil
}

func (p *RealK8sDataProvider) GetClusterName() string {
	return p.clusterName
}

// SortPodIndicators sorts pod indicators by color (RED, YELLOW, GREEN)
func SortPodIndicators(indicators []string) []string {
	// Define color priority (red = 0, yellow = 1, green = 2)
	colorPriority := map[string]int{
		"red":    0,
		"yellow": 1,
		"green":  2,
	}

	// Sort indicators by color
	sort.Slice(indicators, func(i, j int) bool {
		var color1, color2 string
		if strings.Contains(indicators[i], "[red]") {
			color1 = "red"
		} else if strings.Contains(indicators[i], "[yellow]") {
			color1 = "yellow"
		} else {
			color1 = "green"
		}

		if strings.Contains(indicators[j], "[red]") {
			color2 = "red"
		} else if strings.Contains(indicators[j], "[yellow]") {
			color2 = "yellow"
		} else {
			color2 = "green"
		}

		return colorPriority[color1] < colorPriority[color2]
	})

	return indicators
}

// getPodInfo extracts PodInfo from a Kubernetes Pod
func getPodInfo(pod *corev1.Pod) PodInfo {
	podInfo := PodInfo{
		Name:          pod.Name,
		Status:        string(pod.Status.Phase),
		RestartCount:  0,
		ContainerInfo: make(map[string]ContainerInfo),
	}

	// Get container information
	for _, container := range pod.Spec.Containers {
		var containerStatus *corev1.ContainerStatus
		for i := range pod.Status.ContainerStatuses {
			if pod.Status.ContainerStatuses[i].Name == container.Name {
				containerStatus = &pod.Status.ContainerStatuses[i]
				break
			}
		}

		status := "Unknown"
		restartCount := 0
		if containerStatus != nil {
			if containerStatus.State.Running != nil {
				status = "Running"
			} else if containerStatus.State.Waiting != nil {
				status = containerStatus.State.Waiting.Reason
			} else if containerStatus.State.Terminated != nil {
				status = containerStatus.State.Terminated.Reason
			}
			restartCount = int(containerStatus.RestartCount)
			podInfo.RestartCount += restartCount
		}

		podInfo.ContainerInfo[container.Name] = ContainerInfo{
			Status:       status,
			RestartCount: restartCount,
		}
	}

	// Handle terminating state
	if pod.DeletionTimestamp != nil {
		podInfo.Status = "Terminating"
	}

	return podInfo
}

// UpdateNodeData implements K8sDataProvider interface
func (p *RealK8sDataProvider) UpdateNodeData(includeNamespaces, excludeNamespaces map[string]bool) (map[string]NodeData, map[string]map[string][]string, error) {
	ctx := context.Background()

	// Get nodes
	nodes, err := p.client.Clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list nodes: %v", err)
	}

	// Get pods from all namespaces
	pods, err := p.client.Clientset.CoreV1().Pods("").List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list pods: %v", err)
	}

	return p.ProcessNodeData(nodes.Items, pods.Items, includeNamespaces, excludeNamespaces)
}
