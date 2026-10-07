package delayclient

import (
	"testing"

	"github.com/lterrac/edge-autoscaler/pkg/informers"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
)

func newNodeLister(names ...string) corelisters.NodeLister {
	indexer := cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})
	for _, n := range names {
		_ = indexer.Add(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: n}})
	}
	return corelisters.NewNodeLister(indexer)
}

func TestFakeDelayClient(t *testing.T) {
	testcases := []struct {
		description string
		nodes       []string
	}{
		{description: "no nodes", nodes: nil},
		{description: "single node", nodes: []string{"node-a"}},
		{description: "three nodes", nodes: []string{"node-a", "node-b", "node-c"}},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			client := NewFakeClient(informers.Listers{NodeLister: newNodeLister(tt.nodes...)})

			delays, err := client.GetDelays()
			require.NoError(t, err)
			require.Len(t, delays, len(tt.nodes)*len(tt.nodes))

			pairs := make(map[[2]string]bool)
			for _, d := range delays {
				require.Zero(t, d.Latency)
				pairs[[2]string{d.FromNode, d.ToNode}] = true
			}
			for _, from := range tt.nodes {
				for _, to := range tt.nodes {
					require.True(t, pairs[[2]string{from, to}], "missing pair %s -> %s", from, to)
				}
			}
		})
	}
}
