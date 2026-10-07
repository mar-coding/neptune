package controller

import (
	"context"
	"fmt"
	"sort"
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	eafake "github.com/lterrac/edge-autoscaler/pkg/generated/clientset/versioned/fake"
	salisters "github.com/lterrac/edge-autoscaler/pkg/generated/listers/edgeautoscaler/v1alpha1"
	"github.com/lterrac/edge-autoscaler/pkg/informers"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/lterrac/edge-autoscaler/pkg/system-controller/pkg/delayclient"
	"github.com/lterrac/edge-autoscaler/pkg/system-controller/pkg/slpaclient"
	openfaaslisters "github.com/openfaas/faas-netes/pkg/client/listers/openfaas/v1"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	appslisters "k8s.io/client-go/listers/apps/v1"
	corelisters "k8s.io/client-go/listers/core/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
)

const ns = "openfaas-fn"

var communityLabel = ealabels.CommunityLabel.WithNamespace(ns).String()

// systemCluster wires a SystemController to fake clientsets, listers backed by
// indexers, the fake SLPA client and the fake delay client (as in the e2e suite).
type systemCluster struct {
	nodes, configurations, schedules, deployments cache.Indexer

	kube *fake.Clientset
	ea   *eafake.Clientset
}

func newSystemCluster() *systemCluster {
	idx := func() cache.Indexer {
		return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	}
	return &systemCluster{
		nodes: idx(), configurations: idx(), schedules: idx(), deployments: idx(),
		kube: fake.NewSimpleClientset(),
		ea:   eafake.NewSimpleClientset(),
	}
}

func (s *systemCluster) controller() *SystemController {
	l := informers.Listers{
		NodeLister:                   corelisters.NewNodeLister(s.nodes),
		CommunityConfigurationLister: salisters.NewCommunityConfigurationLister(s.configurations),
		CommunityScheduleLister:      salisters.NewCommunityScheduleLister(s.schedules),
		DeploymentLister:             appslisters.NewDeploymentLister(s.deployments),
		PodLister:                    corelisters.NewPodLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})),
		FunctionLister:               openfaaslisters.NewFunctionLister(cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{})),
	}
	return &SystemController{
		edgeAutoscalerClientSet: s.ea,
		kubernetesClientset:     s.kube,
		communityGetter:         slpaclient.NewFakeClient(),
		communityUpdater:        NewCommunityUpdater(s.kube.CoreV1().Nodes().Update, l.NodeLister.List, s.ea),
		delayClient:             delayclient.NewFakeClient(l),
		listers:                 l,
		recorder:                record.NewFakeRecorder(100),
	}
}

func (s *systemCluster) addNode(name string, ready bool, l map[string]string) {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	if l == nil {
		l = map[string]string{}
	}
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l},
		Status:     corev1.NodeStatus{Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: status}}},
	}
	_ = s.nodes.Add(node)
	_ = s.kube.Tracker().Add(node)
}

func (s *systemCluster) addConfiguration(size int64, communities ...string) *eav1alpha1.CommunityConfiguration {
	cc := &eav1alpha1.CommunityConfiguration{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "example-cc", UID: "cc-uid"},
		Spec:       eav1alpha1.CommunityConfigurationSpec{CommunitySize: size, SlpaService: "slpa:4567"},
		Status:     eav1alpha1.CommunityConfigurationStatus{Communities: communities},
	}
	_ = s.configurations.Add(cc)
	_ = s.ea.Tracker().Add(cc)
	return cc
}

// nodeCommunities returns node name -> community label as stored in the API server.
func (s *systemCluster) nodeCommunities(t *testing.T) map[string]string {
	t.Helper()
	nodes, err := s.kube.CoreV1().Nodes().List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	result := map[string]string{}
	for _, n := range nodes.Items {
		result[n.Name] = n.Labels[communityLabel]
	}
	return result
}

func TestSyncCommunityConfiguration(t *testing.T) {
	testcases := []struct {
		description string
		size        int64
		setup       func(s *systemCluster)
		// expectedSizes are the sorted community sizes; the fake SLPA assigns
		// nodes round robin, so membership depends on lister order
		expectedSizes []int
		expected      map[string]string // exact labels, when deterministic
	}{
		{
			description: "all workers fit in one community",
			size:        3,
			setup: func(s *systemCluster) {
				s.addNode("node-a", true, nil)
				s.addNode("node-b", true, nil)
				s.addNode("node-c", true, nil)
			},
			expectedSizes: []int{3},
		},
		{
			description: "workers are split according to the community size",
			size:        2,
			setup: func(s *systemCluster) {
				s.addNode("node-a", true, nil)
				s.addNode("node-b", true, nil)
				s.addNode("node-c", true, nil)
			},
			expectedSizes: []int{1, 2},
		},
		{
			description: "control plane and not ready nodes are excluded and lose stale labels",
			size:        3,
			setup: func(s *systemCluster) {
				s.addNode("master", true, map[string]string{ealabels.MasterNodeLabel: "", communityLabel: "stale"})
				s.addNode("node-a", true, nil)
				s.addNode("node-down", false, map[string]string{communityLabel: "stale"})
			},
			expected: map[string]string{"master": "", "node-a": "community-0", "node-down": ""},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			s := newSystemCluster()
			tt.setup(s)
			s.addConfiguration(tt.size)

			require.NoError(t, s.controller().syncCommunityConfiguration(ns+"/example-cc"))

			labels := s.nodeCommunities(t)
			if tt.expected != nil {
				require.Equal(t, tt.expected, labels)
			}

			members := map[string]int{}
			for _, community := range labels {
				if community != "" {
					members[community]++
				}
			}
			if tt.expectedSizes != nil {
				sizes := []int{}
				for _, n := range members {
					sizes = append(sizes, n)
				}
				sort.Ints(sizes)
				require.Equal(t, tt.expectedSizes, sizes)
			}

			cc, err := s.ea.EdgeautoscalerV1alpha1().CommunityConfigurations(ns).Get(context.TODO(), "example-cc", metav1.GetOptions{})
			require.NoError(t, err)
			status := append([]string{}, cc.Status.Communities...)
			sort.Strings(status)
			expectedStatus := []string{}
			for c := range members {
				expectedStatus = append(expectedStatus, c)
			}
			sort.Strings(expectedStatus)
			require.Equal(t, expectedStatus, status, "status lists exactly the generated communities")
		})
	}
}

func TestSyncCommunityConfigurationErrors(t *testing.T) {
	t.Run("no ready workers", func(t *testing.T) {
		s := newSystemCluster()
		s.addNode("master", true, map[string]string{ealabels.MasterNodeLabel: ""})
		s.addConfiguration(3)
		require.Error(t, s.controller().syncCommunityConfiguration(ns+"/example-cc"))
	})

	t.Run("invalid key is dropped", func(t *testing.T) {
		require.NoError(t, newSystemCluster().controller().syncCommunityConfiguration("a/b/c"))
	})
}

func TestSyncCommunityConfigurationDeletedClearsLabels(t *testing.T) {
	s := newSystemCluster()
	s.addNode("node-a", true, map[string]string{communityLabel: "community-0", "keep": "me"})
	s.addNode("node-b", true, map[string]string{communityLabel: "community-1"})

	// the configuration is not in the lister anymore
	require.NoError(t, s.controller().syncCommunityConfiguration(ns+"/example-cc"))

	require.Equal(t, map[string]string{"node-a": "", "node-b": ""}, s.nodeCommunities(t))
	node, err := s.kube.CoreV1().Nodes().Get(context.TODO(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	require.Equal(t, "me", node.Labels["keep"])
}

func TestSyncCommunitySchedules(t *testing.T) {
	s := newSystemCluster()
	cc := s.addConfiguration(3, "community-0", "community-1")

	// community-1 already has its resources, community-2 is obsolete
	for _, name := range []string{"community-1", "community-2"} {
		cs := NewCommunitySchedule(ns, name, cc)
		_ = s.schedules.Add(cs)
		_ = s.ea.Tracker().Add(cs)
		dp := NewCommunityController(ns, name, cc)
		_ = s.deployments.Add(dp)
		_ = s.kube.Tracker().Add(dp)
	}
	// a deployment that is not a community controller must be left alone
	other := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "community-3"}}
	_ = s.deployments.Add(other)
	_ = s.kube.Tracker().Add(other)

	require.NoError(t, s.controller().syncCommunitySchedules(ns+"/example-cc"))

	schedules, err := s.ea.EdgeautoscalerV1alpha1().CommunitySchedules(ns).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	scheduleNames := []string{}
	for _, cs := range schedules.Items {
		scheduleNames = append(scheduleNames, cs.Name)
	}
	sort.Strings(scheduleNames)
	require.Equal(t, []string{"community-0", "community-1"}, scheduleNames)

	deployments, err := s.kube.AppsV1().Deployments(ns).List(context.TODO(), metav1.ListOptions{})
	require.NoError(t, err)
	deploymentNames := []string{}
	for _, dp := range deployments.Items {
		deploymentNames = append(deploymentNames, dp.Name)
	}
	sort.Strings(deploymentNames)
	require.Equal(t, []string{"community-0", "community-1", "community-3"}, deploymentNames)
}

func TestSyncCommunitySchedulesMissingConfiguration(t *testing.T) {
	s := newSystemCluster()
	require.NoError(t, s.controller().syncCommunitySchedules(ns+"/example-cc"))
	require.NoError(t, s.controller().syncCommunitySchedules("a/b/c"))
}

// End to end at unit level: communities computed from nodes become schedules and
// community controllers.
func TestCommunitiesToSchedules(t *testing.T) {
	s := newSystemCluster()
	for i := 0; i < 4; i++ {
		s.addNode(fmt.Sprintf("node-%d", i), true, nil)
	}
	s.addConfiguration(2)
	c := s.controller()

	require.NoError(t, c.syncCommunityConfiguration(ns+"/example-cc"))

	// propagate the updated status to the lister, as the informer would
	cc, err := s.ea.EdgeautoscalerV1alpha1().CommunityConfigurations(ns).Get(context.TODO(), "example-cc", metav1.GetOptions{})
	require.NoError(t, err)
	require.NoError(t, s.configurations.Update(cc))
	require.Len(t, cc.Status.Communities, 2)

	require.NoError(t, c.syncCommunitySchedules(ns+"/example-cc"))

	for _, community := range cc.Status.Communities {
		_, err := s.ea.EdgeautoscalerV1alpha1().CommunitySchedules(ns).Get(context.TODO(), community, metav1.GetOptions{})
		require.NoError(t, err, "schedule for %s", community)
		_, err = s.kube.AppsV1().Deployments(ns).Get(context.TODO(), community, metav1.GetOptions{})
		require.NoError(t, err, "community controller for %s", community)
	}
}

func TestClearNodesLabels(t *testing.T) {
	s := newSystemCluster()
	s.addNode("node-a", true, map[string]string{communityLabel: "community-0", ealabels.CommunityLabel.WithNamespace("other").String(): "x"})
	updater := NewCommunityUpdater(s.kube.CoreV1().Nodes().Update, corelisters.NewNodeLister(s.nodes).List, s.ea)

	require.NoError(t, updater.ClearNodesLabels(ns))

	node, err := s.kube.CoreV1().Nodes().Get(context.TODO(), "node-a", metav1.GetOptions{})
	require.NoError(t, err)
	require.NotContains(t, node.Labels, communityLabel)
	require.Equal(t, "x", node.Labels[ealabels.CommunityLabel.WithNamespace("other").String()], "other namespaces are untouched")
}
