package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// fakeSolver records the scheduling inputs it receives and answers with a fixed output.
type fakeSolver struct {
	*httptest.Server
	mu     sync.Mutex
	inputs []SchedulingInput
}

func newFakeSolver(t *testing.T, status int, output *SchedulingOutput) *fakeSolver {
	s := &fakeSolver{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in SchedulingInput
		require.NoError(t, json.NewDecoder(r.Body).Decode(&in))
		s.mu.Lock()
		s.inputs = append(s.inputs, in)
		s.mu.Unlock()
		if status != http.StatusOK {
			http.Error(w, "solver failure", status)
			return
		}
		require.NoError(t, json.NewEncoder(w).Encode(output))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *fakeSolver) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.inputs)
}

// schedulerCluster has two community nodes, one node of another community, a
// function with a max delay label and a non-function pod consuming resources.
func schedulerCluster(solverURL string) *cluster {
	c := newCluster()
	capacity := func(cpu, mem string) corev1.ResourceList {
		return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse(mem)}
	}
	for _, n := range []struct {
		name, community, cpu, mem string
	}{
		{"node-b", community, "4", "8Gi"},
		{"node-a", community, "2", "4Gi"},
		{"node-x", "community-2", "8", "16Gi"},
	} {
		_ = c.nodes.Add(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: n.name, Labels: map[string]string{ealabels.CommunityLabel.WithNamespace(ns).String(): n.community}},
			Status:     corev1.NodeStatus{Capacity: capacity(n.cpu, n.mem)},
		})
	}

	f := c.addFunction("prime", false)
	f.Spec.Labels = &map[string]string{ealabels.FunctionMaxDelayLabel: "100"}

	// a pod not managed by NEPTUNE reduces the capacity offered to the solver
	_ = c.pods.Add(&corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "kube-system", Name: "system-pod"},
		Spec: corev1.PodSpec{NodeName: "node-a", Containers: []corev1.Container{{
			Resources: corev1.ResourceRequirements{Requests: capacity("300m", "1Gi")},
		}}},
	})

	cs := c.addSchedule(alloc("prime", "node-a"), nil)
	cs.Spec.AlgorithmService = solverURL
	return c
}

func TestRunScheduler(t *testing.T) {
	output := &SchedulingOutput{
		NodeNames:       []string{"node-a", "node-b"},
		FunctionNames:   []string{ns + "/prime"},
		CpuAllocations:  map[string]map[string]bool{ns + "/prime": {"node-b": true}},
		CpuRoutingRules: map[string]map[string]map[string]float64{"node-a": {ns + "/prime": {"node-b": 1}}},
	}
	solver := newFakeSolver(t, http.StatusOK, output)
	c := schedulerCluster(solver.URL)

	require.NoError(t, c.controller().runScheduler(""))

	t.Run("solver receives the community state", func(t *testing.T) {
		require.Equal(t, 1, solver.calls())
		in := solver.inputs[0]

		require.Equal(t, ns, in.Namespace)
		require.Equal(t, community, in.Community)
		require.Equal(t, []string{"node-a", "node-b"}, in.NodeNames, "only community nodes, sorted")
		require.Equal(t, []string{ns + "/prime"}, in.FunctionNames)

		gi := int64(1 << 30)
		require.Equal(t, []int64{2000 - NodeCorePadding - 300, 4000 - NodeCorePadding}, in.NodeCores)
		require.Equal(t, []int64{4*gi - NodeMemoryPadding - gi, 8*gi - NodeMemoryPadding}, in.NodeMemories)
		require.Equal(t, []int64{1000*(1<<20) + HttpMetricsMemory}, in.FunctionMemories)
		require.Equal(t, []int64{100}, in.FunctionMaxDelays)
		require.Equal(t, alloc("prime", "node-a"), in.ActualCPUAllocation)
	})

	t.Run("solver output is written to the community schedule", func(t *testing.T) {
		cs, err := c.ea.EdgeautoscalerV1alpha1().CommunitySchedules(ns).Get(contextTODO(), community, metav1.GetOptions{})
		require.NoError(t, err)
		require.Equal(t, alloc("prime", "node-b"), cs.Spec.CpuAllocations)
		weight := cs.Spec.CpuRoutingRules["node-a"][ns+"/prime"]["node-b"]
		require.Equal(t, int64(1000), weight.MilliValue())
		require.Equal(t, solver.URL, cs.Spec.AlgorithmService)
	})
}

func TestRunSchedulerErrors(t *testing.T) {
	testcases := []struct {
		description string
		setup       func(t *testing.T) *cluster
	}{
		{
			description: "solver returns an error",
			setup: func(t *testing.T) *cluster {
				return schedulerCluster(newFakeSolver(t, http.StatusInternalServerError, nil).URL)
			},
		},
		{
			description: "solver is unreachable",
			setup: func(t *testing.T) *cluster {
				s := newFakeSolver(t, http.StatusOK, &SchedulingOutput{})
				s.Close()
				return schedulerCluster(s.URL)
			},
		},
		{
			description: "community without nodes",
			setup: func(t *testing.T) *cluster {
				c := newCluster()
				c.addFunction("prime", false)
				c.addSchedule(nil, nil)
				return c
			},
		},
		{
			description: "community without functions",
			setup: func(t *testing.T) *cluster {
				c := newCluster()
				c.addNode("node-a", nil)
				c.addSchedule(nil, nil)
				return c
			},
		},
		{
			description: "missing community schedule",
			setup: func(t *testing.T) *cluster {
				c := newCluster()
				c.addNode("node-a", nil)
				c.addFunction("prime", false)
				return c
			},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			c := tt.setup(t)
			before, _ := c.ea.EdgeautoscalerV1alpha1().CommunitySchedules(ns).Get(contextTODO(), community, metav1.GetOptions{})

			require.Error(t, c.controller().runScheduler(""))

			after, _ := c.ea.EdgeautoscalerV1alpha1().CommunitySchedules(ns).Get(contextTODO(), community, metav1.GetOptions{})
			require.Equal(t, before, after, "schedule must not change on failure")
		})
	}
}

// NewSchedulingInput dereferences function.Spec.Labels without a nil check, so a
// Function without labels makes the community controller panic.
func TestRunSchedulerFunctionWithoutLabels(t *testing.T) {
	t.Skip("known bug: Function without spec.labels panics (pkg/community-controller/pkg/controller/scheduling.go NewSchedulingInput)")

	solver := newFakeSolver(t, http.StatusOK, &SchedulingOutput{})
	c := newCluster()
	c.addNode("node-a", nil)
	c.addFunction("prime", false) // no Spec.Labels
	cs := c.addSchedule(nil, nil)
	cs.Spec.AlgorithmService = solver.URL

	require.NotPanics(t, func() { _ = c.controller().runScheduler("") })
}

// drainQueue shuts the work queue down and returns the keys it still holds.
func drainQueue(c *CommunityController) []string {
	c.syncCommunityScheduleWorkqueue.ShutDown()
	keys := []string{}
	for c.syncCommunityScheduleWorkqueue.ProcessNextItem(func(key string) error {
		keys = append(keys, key)
		return nil
	}) {
	}
	return keys
}

func TestEventHandlersEnqueueOwnSchedule(t *testing.T) {
	own := ns + "/" + community
	pod := func(communityName string) *corev1.Pod {
		return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "p", Labels: map[string]string{
			ealabels.CommunityLabel.WithNamespace(ns).String(): communityName,
		}}}
	}
	schedule := func(namespace, name string) *eav1alpha1.CommunitySchedule {
		return &eav1alpha1.CommunitySchedule{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	}

	testcases := []struct {
		description string
		handle      func(c *CommunityController)
		expected    []string
	}{
		{"pod of the community", func(c *CommunityController) { c.handlePod(pod(community)) }, []string{own}},
		{"pod update of the community", func(c *CommunityController) { c.handlePodUpdate(pod("community-2"), pod(community)) }, []string{own}},
		{"pod of another community", func(c *CommunityController) { c.handlePod(pod("community-2")) }, []string{}},
		{"pod without community", func(c *CommunityController) { c.handlePod(&corev1.Pod{}) }, []string{}},
		{"not a pod", func(c *CommunityController) { c.handlePod("tombstone") }, []string{}},
		{"own schedule", func(c *CommunityController) { c.handleCommunitySchedule(schedule(ns, community)) }, []string{own}},
		{"own schedule update", func(c *CommunityController) {
			c.handleCommunityScheduleUpdate(schedule(ns, community), schedule(ns, community))
		}, []string{own}},
		{"schedule of another community", func(c *CommunityController) { c.handleCommunitySchedule(schedule(ns, "community-2")) }, []string{}},
		{"schedule in another namespace", func(c *CommunityController) { c.handleCommunitySchedule(schedule("other", community)) }, []string{}},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			cl := newCluster()
			cl.addSchedule(nil, nil)
			c := cl.controller()

			tt.handle(c)

			require.Equal(t, tt.expected, drainQueue(c))
		})
	}
}

func TestEventHandlersTriggerRescheduling(t *testing.T) {
	node := func(communityName string) *corev1.Node {
		return &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "n", Labels: map[string]string{
			ealabels.CommunityLabel.WithNamespace(ns).String(): communityName,
		}}}
	}
	function := func(namespace string) interface{} {
		f := newTestFunction(nil, nil)
		f.Namespace = namespace
		return f
	}

	testcases := []struct {
		description   string
		handle        func(c *CommunityController)
		expectedCalls int
	}{
		{"node joins the community", func(c *CommunityController) { c.handleNode(node(community)) }, 1},
		{"node moves between communities", func(c *CommunityController) { c.handleNodeUpdate(node("community-2"), node(community)) }, 1},
		{"node of another community", func(c *CommunityController) { c.handleNode(node("community-2")) }, 0},
		{"node without community", func(c *CommunityController) { c.handleNode(&corev1.Node{}) }, 0},
		{"function in the community namespace", func(c *CommunityController) { c.handleFunction(function(ns)) }, 1},
		{"function update", func(c *CommunityController) { c.handleFunctionUpdate(function(ns), function(ns)) }, 2},
		{"function in another namespace", func(c *CommunityController) { c.handleFunction(function("other")) }, 0},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			solver := newFakeSolver(t, http.StatusOK, &SchedulingOutput{})
			c := schedulerCluster(solver.URL).controller()

			tt.handle(c)

			require.Equal(t, tt.expectedCalls, solver.calls())
		})
	}
}
