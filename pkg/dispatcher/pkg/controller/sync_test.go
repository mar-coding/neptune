package controller

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"sync"
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	"github.com/lterrac/edge-autoscaler/pkg/apiutils"
	"github.com/lterrac/edge-autoscaler/pkg/dispatcher/pkg/balancer"
	salisters "github.com/lterrac/edge-autoscaler/pkg/generated/listers/edgeautoscaler/v1alpha1"
	"github.com/lterrac/edge-autoscaler/pkg/informers"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/lterrac/edge-autoscaler/pkg/metrics"
	openfaasv1 "github.com/openfaas/faas-netes/pkg/apis/openfaas/v1"
	openfaaslisters "github.com/openfaas/faas-netes/pkg/client/listers/openfaas/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

const (
	namespace = "openfaas-fn"
	community = "community-1"
	function  = "prime-numbers"
	localNode = "node-a"
)

var functionKey = namespace + "/" + function

type fixture struct {
	pods      cache.Indexer
	nodes     cache.Indexer
	schedules cache.Indexer
	functions cache.Indexer
}

func newFixture() *fixture {
	newIndexer := func() cache.Indexer {
		return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	}
	f := &fixture{pods: newIndexer(), nodes: newIndexer(), schedules: newIndexer(), functions: newIndexer()}
	_ = f.functions.Add(&openfaasv1.Function{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: function}})
	return f
}

func (f *fixture) controller(node string) *LoadBalancerController {
	l := informers.Listers{
		PodLister:               corelisters.NewPodLister(f.pods),
		NodeLister:              corelisters.NewNodeLister(f.nodes),
		CommunityScheduleLister: salisters.NewCommunityScheduleLister(f.schedules),
		FunctionLister:          openfaaslisters.NewFunctionLister(f.functions),
	}
	return &LoadBalancerController{
		listers:    l,
		resGetter:  apiutils.NewResourceGetter(l.Pods, l.Functions, l.NodeLister),
		recorder:   record.NewFakeRecorder(100),
		metricChan: make(chan metrics.RawResponseTime, 100),
		balancers:  sync.Map{},
		node:       node,
	}
}

func (f *fixture) addNode(name, communityName string) {
	l := map[string]string{}
	if communityName != "" {
		l[ealabels.CommunityLabel.WithNamespace(namespace).String()] = communityName
	}
	_ = f.nodes.Add(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}})
}

func (f *fixture) addPod(name, node, ip string, ready bool) {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	_ = f.pods.Add(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels: map[string]string{
				ealabels.FunctionNamespaceLabel: namespace,
				ealabels.FunctionNameLabel:      function,
				ealabels.NodeLabel:              node,
			},
		},
		Status: corev1.PodStatus{
			PodIP:      ip,
			Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}},
		},
	})
}

// addSchedule adds a CommunitySchedule whose CPU routing rules send the function's
// traffic from each source node to the given destination nodes with weight 1.
func (f *fixture) addSchedule(name string, routes map[string][]string) {
	rules := eav1alpha1.CommunitySourceRoutingRule{}
	for source, destinations := range routes {
		d := eav1alpha1.CommunityDestinationRoutingRule{}
		for _, dest := range destinations {
			d[dest] = resource.MustParse("1")
		}
		rules[source] = eav1alpha1.CommunityFunctionRoutingRule{functionKey: d}
	}
	_ = f.schedules.Add(&eav1alpha1.CommunitySchedule{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name},
		Spec:       eav1alpha1.CommunityScheduleSpec{CpuRoutingRules: rules},
	})
}

// backends returns the sorted hosts of the balancer of the test function, or nil if none exists.
func backends(c *LoadBalancerController) []string {
	value, ok := c.balancers.Load(functionKey)
	if !ok {
		return nil
	}
	hosts := []string{}
	for _, u := range value.(*balancer.LoadBalancer).ServerPoolDiff([]*url.URL{}) {
		hosts = append(hosts, u.Host)
	}
	sort.Strings(hosts)
	return hosts
}

func TestSyncCommunitySchedule(t *testing.T) {
	testcases := []struct {
		description      string
		setup            func(f *fixture)
		key              string
		expectBalancer   bool
		expectedBackends []string
	}{
		{
			description: "ready pods on destination nodes become backends on port 8000",
			setup: func(f *fixture) {
				f.addNode(localNode, community)
				f.addNode("node-b", community)
				f.addPod("p1", localNode, "10.0.0.1", true)
				f.addPod("p2", "node-b", "10.0.0.2", true)
				f.addPod("p3", "node-c", "10.0.0.3", true)
				f.addSchedule(community, map[string][]string{localNode: {localNode, "node-b"}})
			},
			key:              namespace + "/" + community,
			expectBalancer:   true,
			expectedBackends: []string{"10.0.0.1:8000", "10.0.0.2:8000"},
		},
		{
			description: "not ready pods block backend registration",
			setup: func(f *fixture) {
				f.addNode(localNode, community)
				f.addPod("p1", localNode, "10.0.0.1", false)
				f.addSchedule(community, map[string][]string{localNode: {localNode}})
			},
			key:              namespace + "/" + community,
			expectBalancer:   true,
			expectedBackends: []string{},
		},
		{
			description: "node outside any community is ignored",
			setup: func(f *fixture) {
				f.addNode(localNode, "")
				f.addPod("p1", localNode, "10.0.0.1", true)
				f.addSchedule(community, map[string][]string{localNode: {localNode}})
			},
			key:            namespace + "/" + community,
			expectBalancer: false,
		},
		{
			description: "schedule of another community is ignored",
			setup: func(f *fixture) {
				f.addNode(localNode, "community-2")
				f.addPod("p1", localNode, "10.0.0.1", true)
				f.addSchedule(community, map[string][]string{localNode: {localNode}})
			},
			key:            namespace + "/" + community,
			expectBalancer: false,
		},
		{
			description: "missing schedule is not an error",
			setup: func(f *fixture) {
				f.addNode(localNode, community)
			},
			key:            namespace + "/" + community,
			expectBalancer: false,
		},
		{
			description:    "missing local node is not an error",
			setup:          func(f *fixture) {},
			key:            namespace + "/" + community,
			expectBalancer: false,
		},
		{
			description:    "invalid key is not an error",
			setup:          func(f *fixture) {},
			key:            "a/b/c",
			expectBalancer: false,
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			f := newFixture()
			tt.setup(f)
			c := f.controller(localNode)

			require.NoError(t, c.syncCommunitySchedule(tt.key))

			if !tt.expectBalancer {
				_, ok := c.balancers.Load(functionKey)
				require.False(t, ok)
				return
			}
			require.Equal(t, tt.expectedBackends, backends(c))
		})
	}
}

func TestSyncCommunityScheduleRemovesStaleBackends(t *testing.T) {
	f := newFixture()
	f.addNode(localNode, community)
	f.addNode("node-b", community)
	f.addPod("p1", localNode, "10.0.0.1", true)
	f.addPod("p2", "node-b", "10.0.0.2", true)
	f.addSchedule(community, map[string][]string{localNode: {localNode, "node-b"}})
	c := f.controller(localNode)

	require.NoError(t, c.syncCommunitySchedule(namespace+"/"+community))
	require.Equal(t, []string{"10.0.0.1:8000", "10.0.0.2:8000"}, backends(c))

	// the solver moves all traffic to the local node
	f.addSchedule(community, map[string][]string{localNode: {localNode}})
	require.NoError(t, c.syncCommunitySchedule(namespace+"/"+community))
	require.Equal(t, []string{"10.0.0.1:8000"}, backends(c))
}

// When the local node is not a source in the routing rules, syncRoutingRules keeps
// the last source visited by the map iteration and applies that node's rules.
// Remove the skip once the source lookup is fixed.
func TestSyncRoutingRulesIgnoresOtherSources(t *testing.T) {
	t.Skip("known bug: rules of another source node are applied (pkg/dispatcher/pkg/controller/sync.go syncRoutingRules)")

	f := newFixture()
	f.addNode(localNode, community)
	f.addPod("p2", "node-b", "10.0.0.2", true)
	f.addSchedule(community, map[string][]string{"node-b": {"node-b"}})
	c := f.controller(localNode)

	require.NoError(t, c.syncCommunitySchedule(namespace+"/"+community))
	_, ok := c.balancers.Load(functionKey)
	require.False(t, ok)
}

func TestPodReadiness(t *testing.T) {
	pod := func(status corev1.ConditionStatus) *corev1.Pod {
		return &corev1.Pod{Status: corev1.PodStatus{Conditions: []corev1.PodCondition{
			{Type: corev1.PodScheduled, Status: corev1.ConditionTrue},
			{Type: corev1.PodReady, Status: status},
		}}}
	}

	testcases := []struct {
		description string
		pods        []*corev1.Pod
		expected    bool
	}{
		{description: "no pods", pods: nil, expected: true},
		{description: "all ready", pods: []*corev1.Pod{pod(corev1.ConditionTrue), pod(corev1.ConditionTrue)}, expected: true},
		{description: "one not ready", pods: []*corev1.Pod{pod(corev1.ConditionTrue), pod(corev1.ConditionFalse)}, expected: false},
		{description: "unknown readiness", pods: []*corev1.Pod{pod(corev1.ConditionUnknown)}, expected: false},
		{description: "no ready condition", pods: []*corev1.Pod{{}}, expected: false},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			require.Equal(t, tt.expected, ArePodsReady(tt.pods))
		})
	}
}

func TestNamespaceNameFunction(t *testing.T) {
	testcases := []struct {
		path     string
		expected string
	}{
		{path: "/function/openfaas-fn/prime-numbers", expected: "openfaas-fn/prime-numbers"},
		{path: "/function/openfaas-fn/prime-numbers/prime/1000", expected: "openfaas-fn/prime-numbers"},
		{path: "/gateway/function/ns/fn/", expected: "ns/fn"},
	}

	for _, tt := range testcases {
		t.Run(tt.path, func(t *testing.T) {
			u, err := url.Parse("http://dispatcher" + tt.path)
			require.NoError(t, err)
			require.Equal(t, tt.expected, NamespaceNameFunction(u))
		})
	}
}

func TestEnqueueRequestWithoutBalancer(t *testing.T) {
	c := newFixture().controller(localNode)

	req := httptest.NewRequest(http.MethodGet, "/function/openfaas-fn/missing/", nil)
	rec := httptest.NewRecorder()

	c.enqueueRequest(rec, req)

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), fmt.Sprintf("no balancer found for function %s", "openfaas-fn/missing"))
}
