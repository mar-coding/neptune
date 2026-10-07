package apiutils

import (
	"fmt"
	"testing"

	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/lterrac/edge-autoscaler/pkg/system-controller/pkg/delayclient"
	openfaasv1 "github.com/openfaas/faas-netes/pkg/apis/openfaas/v1"
	openfaaslisters "github.com/openfaas/faas-netes/pkg/client/listers/openfaas/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

const namespace = "openfaas-fn"

// fakeDelayClient returns a fixed set of delays or an error.
type fakeDelayClient struct {
	delays []*delayclient.NodeDelay
	err    error
}

func (f fakeDelayClient) GetDelays() ([]*delayclient.NodeDelay, error) {
	return f.delays, f.err
}

func newIndexer(objs ...interface{}) cache.Indexer {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	for _, o := range objs {
		_ = indexer.Add(o)
	}
	return indexer
}

func newFunction(name string) *openfaasv1.Function {
	return &openfaasv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
}

func newFunctionPod(name, function, node string, gpu bool) *corev1.Pod {
	l := map[string]string{
		ealabels.FunctionNamespaceLabel: namespace,
		ealabels.FunctionNameLabel:      function,
		ealabels.NodeLabel:              node,
	}
	if gpu {
		l[ealabels.GpuFunctionLabel] = ""
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: l}}
}

func newNode(name, community string) *corev1.Node {
	l := map[string]string{}
	if community != "" {
		l[ealabels.CommunityLabel.WithNamespace(namespace).String()] = community
	}
	return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}}
}

func newGetter(objs ...interface{}) *ResourceGetter {
	var pods, functions, nodes []interface{}
	for _, o := range objs {
		switch o.(type) {
		case *corev1.Pod:
			pods = append(pods, o)
		case *openfaasv1.Function:
			functions = append(functions, o)
		case *corev1.Node:
			nodes = append(nodes, o)
		}
	}
	podLister := corelisters.NewPodLister(newIndexer(pods...))
	functionLister := openfaaslisters.NewFunctionLister(newIndexer(functions...))
	return NewResourceGetter(podLister.Pods, functionLister.Functions, corelisters.NewNodeLister(newIndexer(nodes...)))
}

func podNames(pods []*corev1.Pod) []string {
	names := make([]string, 0, len(pods))
	for _, p := range pods {
		names = append(names, p.Name)
	}
	return names
}

func TestGetPodsOfFunctionInNode(t *testing.T) {
	getter := newGetter(
		newFunctionPod("cpu-node-a", "prime", "node-a", false),
		newFunctionPod("gpu-node-a", "prime", "node-a", true),
		newFunctionPod("cpu-node-b", "prime", "node-b", false),
		newFunctionPod("other-node-a", "other", "node-a", false),
	)

	testcases := []struct {
		description string
		node        string
		gpu         bool
		expected    []string
	}{
		{
			description: "cpu lookup returns every pod of the function on the node",
			node:        "node-a",
			gpu:         false,
			expected:    []string{"cpu-node-a", "gpu-node-a"},
		},
		{
			description: "gpu lookup returns only gpu pods",
			node:        "node-a",
			gpu:         true,
			expected:    []string{"gpu-node-a"},
		},
		{
			description: "node without pods of the function",
			node:        "node-c",
			gpu:         false,
			expected:    []string{},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			pods, err := getter.GetPodsOfFunctionInNode(newFunction("prime"), tt.node, tt.gpu)
			require.NoError(t, err)
			require.ElementsMatch(t, tt.expected, podNames(pods))
		})
	}
}

func TestGetNodeDelays(t *testing.T) {
	nodes := []string{"node-a", "node-b", "node-c"}

	testcases := []struct {
		description string
		client      delayclient.DelayClient
		expected    [][]int64
		expectError bool
	}{
		{
			description: "delays are placed in the matrix by node index",
			client: fakeDelayClient{delays: []*delayclient.NodeDelay{
				{FromNode: "node-a", ToNode: "node-b", Latency: 10},
				{FromNode: "node-b", ToNode: "node-a", Latency: 12},
				{FromNode: "node-b", ToNode: "node-c", Latency: 20.9},
				{FromNode: "node-c", ToNode: "node-c", Latency: 1},
			}},
			expected: [][]int64{
				{0, 10, 0},
				{12, 0, 20},
				{0, 0, 1},
			},
		},
		{
			description: "no delays produces a zero matrix",
			client:      fakeDelayClient{},
			expected: [][]int64{
				{0, 0, 0},
				{0, 0, 0},
				{0, 0, 0},
			},
		},
		{
			description: "client error is returned",
			client:      fakeDelayClient{err: fmt.Errorf("db unavailable")},
			expectError: true,
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			delays, err := newGetter().GetNodeDelays(tt.client, nodes)
			if tt.expectError {
				require.Error(t, err)
				require.Nil(t, delays)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, delays)
		})
	}
}

// Delays from or to nodes that are not in the requested list (e.g. the control
// plane, which the system controller excludes) are currently mapped onto index 0
// and overwrite real values. Remove the skip once GetNodeDelays ignores them.
func TestGetNodeDelaysIgnoresUnknownNodes(t *testing.T) {
	t.Skip("known bug: unknown node names are mapped to index 0 (pkg/apiutils/getter.go GetNodeDelays)")

	client := fakeDelayClient{delays: []*delayclient.NodeDelay{
		{FromNode: "node-a", ToNode: "node-b", Latency: 10},
		{FromNode: "master", ToNode: "node-b", Latency: 99},
	}}

	delays, err := newGetter().GetNodeDelays(client, []string{"node-a", "node-b"})
	require.NoError(t, err)
	require.Equal(t, [][]int64{{0, 10}, {0, 0}}, delays)
}

func TestGetWorkload(t *testing.T) {
	getter := newGetter(
		newNode("node-a", "community-1"),
		newNode("node-b", "community-1"),
		newNode("node-c", "community-2"),
		newNode("node-d", ""),
		newFunction("f1"),
		newFunction("f2"),
		newFunction("f3"),
	)

	testcases := []struct {
		description   string
		community     string
		expectedNodes int
	}{
		{description: "community with two nodes", community: "community-1", expectedNodes: 2},
		{description: "community with one node", community: "community-2", expectedNodes: 1},
		{description: "unknown community", community: "missing", expectedNodes: 0},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			workload, err := getter.GetWorkload(tt.community, namespace)
			require.NoError(t, err)
			require.Len(t, workload, tt.expectedNodes)
			for _, row := range workload {
				require.Equal(t, []int64{0, 0, 0}, row)
			}
		})
	}
}

func TestGetMaxDelays(t *testing.T) {
	getter := newGetter(newFunction("f1"), newFunction("f2"))

	delays, err := getter.GetMaxDelays(namespace)
	require.NoError(t, err)
	require.Equal(t, []int64{0, 0}, delays)

	delays, err = getter.GetMaxDelays("empty")
	require.NoError(t, err)
	require.Empty(t, delays)
}
