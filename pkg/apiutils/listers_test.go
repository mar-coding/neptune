package apiutils

import (
	"testing"

	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	openfaaslisters "github.com/openfaas/faas-netes/pkg/client/listers/openfaas/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	corelisters "k8s.io/client-go/listers/core/v1"
)

func TestNewPodGetter(t *testing.T) {
	_, err := NewPodGetter(nil)
	require.Error(t, err)

	getter, err := NewPodGetter(corelisters.NewPodLister(newIndexer()).Pods)
	require.NoError(t, err)
	require.NotNil(t, getter)
}

func TestListersGetPods(t *testing.T) {
	unlabelled := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
		Namespace: namespace,
		Name:      "unmanaged",
		Labels:    map[string]string{ealabels.NodeLabel: "node-b"},
	}}
	podLister := corelisters.NewPodLister(newIndexer(
		newFunctionPod("prime-a", "prime", "node-a", false),
		newFunctionPod("other-a", "other", "node-a", false),
		newFunctionPod("prime-b", "prime", "node-b", false),
		unlabelled,
	))
	functionLister := openfaaslisters.NewFunctionLister(newIndexer(newFunction("prime")))
	nodeLister := corelisters.NewNodeLister(newIndexer(newNode("node-a", "")))

	l := NewListers(podLister.Pods, functionLister.Functions, nodeLister)

	t.Run("all function pods in a node", func(t *testing.T) {
		pods, err := l.GetPodsOfAllFunctionInNode(namespace, "node-a")
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"prime-a", "other-a"}, podNames(pods))

		pods, err = l.GetPodsOfAllFunctionInNode(namespace, "node-b")
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"prime-b", "unmanaged"}, podNames(pods))
	})

	t.Run("pods of a single function in a node", func(t *testing.T) {
		pods, err := l.GetPodsOfFunctionInNode(newFunction("prime"), "node-a")
		require.NoError(t, err)
		require.ElementsMatch(t, []string{"prime-a"}, podNames(pods))
	})

	t.Run("accessors return the wrapped listers", func(t *testing.T) {
		f, err := l.Functions(namespace).Get("prime")
		require.NoError(t, err)
		require.Equal(t, "prime", f.Name)

		n, err := l.Nodes().Get("node-a")
		require.NoError(t, err)
		require.Equal(t, "node-a", n.Name)

		pods, err := l.Pods(namespace).List(labels.Everything())
		require.NoError(t, err)
		require.Len(t, pods, 4)
	})
}
