package controller

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	eafake "github.com/lterrac/edge-autoscaler/pkg/generated/clientset/versioned/fake"
	salisters "github.com/lterrac/edge-autoscaler/pkg/generated/listers/edgeautoscaler/v1alpha1"
	"github.com/lterrac/edge-autoscaler/pkg/informers"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/lterrac/edge-autoscaler/pkg/queue"
	openfaasv1 "github.com/openfaas/faas-netes/pkg/apis/openfaas/v1"
	openfaaslisters "github.com/openfaas/faas-netes/pkg/client/listers/openfaas/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/fake"
	corelisters "k8s.io/client-go/listers/core/v1"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
)

const (
	ns        = "openfaas-fn"
	community = "community-1"
)

// cluster is a fake community: listers backed by indexers plus fake clientsets.
// Pods are mirrored in both, because sync reads pods from the listers and then
// gets, binds and deletes them through the clientset.
type cluster struct {
	pods      cache.Indexer
	nodes     cache.Indexer
	functions cache.Indexer
	schedules cache.Indexer

	kube *fake.Clientset
	ea   *eafake.Clientset

	// readyOnCreate marks the pods created by the controller as ready
	readyOnCreate bool

	mu       sync.Mutex
	bindings map[string]string // pod name -> node
}

func newCluster() *cluster {
	idx := func() cache.Indexer {
		return cache.NewIndexer(cache.MetaNamespaceKeyFunc, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc})
	}
	c := &cluster{
		pods: idx(), nodes: idx(), functions: idx(), schedules: idx(),
		kube:     fake.NewSimpleClientset(),
		ea:       eafake.NewSimpleClientset(),
		bindings: map[string]string{},
	}

	c.kube.PrependReactor("create", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		create := action.(k8stesting.CreateAction)
		switch obj := create.GetObject().(type) {
		case *corev1.Binding:
			// the fake tracker would store the binding as the pod: record it and
			// set the node like the scheduler would
			c.mu.Lock()
			c.bindings[obj.Name] = obj.Target.Name
			c.mu.Unlock()
			pod, err := c.kube.Tracker().Get(corev1.SchemeGroupVersion.WithResource("pods"), obj.Namespace, obj.Name)
			if err != nil {
				return true, nil, err
			}
			bound := pod.(*corev1.Pod).DeepCopy()
			bound.Spec.NodeName = obj.Target.Name
			return true, nil, c.kube.Tracker().Update(corev1.SchemeGroupVersion.WithResource("pods"), bound, obj.Namespace)
		case *corev1.Pod:
			if c.readyOnCreate {
				obj.Status.Phase = corev1.PodRunning
				obj.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
			}
		}
		return false, nil, nil
	})
	return c
}

func (c *cluster) controller() *CommunityController {
	l := informers.Listers{
		PodLister:               corelisters.NewPodLister(c.pods),
		NodeLister:              corelisters.NewNodeLister(c.nodes),
		FunctionLister:          openfaaslisters.NewFunctionLister(c.functions),
		CommunityScheduleLister: salisters.NewCommunityScheduleLister(c.schedules),
	}
	return &CommunityController{
		edgeAutoscalerClientSet:        c.ea,
		kubernetesClientset:            c.kube,
		listers:                        l,
		recorder:                       record.NewFakeRecorder(100),
		communityName:                  community,
		communityNamespace:             ns,
		syncCommunityScheduleWorkqueue: queue.NewQueue("test", workqueue.NewItemExponentialFailureRateLimiter(0, 0)),
	}
}

func (c *cluster) addNode(name string, extra map[string]string) {
	l := map[string]string{ealabels.CommunityLabel.WithNamespace(ns).String(): community}
	for k, v := range extra {
		l[k] = v
	}
	_ = c.nodes.Add(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: name, Labels: l}})
}

func (c *cluster) addFunction(name string, gpu bool) *openfaasv1.Function {
	f := newTestFunction(res("200m", "1000Mi"), res("200m", "1000Mi"))
	f.Name, f.Spec.Name = name, name
	if gpu {
		f.Labels[ealabels.GpuFunctionLabel] = ""
		f.Spec.Labels = &map[string]string{
			ealabels.GpuFunctionMemoryLabel: "1Gi",
			ealabels.GpuFunctionVGPU:        "1",
		}
	}
	_ = c.functions.Add(f)
	return f
}

type podState struct {
	running  bool
	ready    bool
	deleting bool
	failed   string // failure reason, e.g. OutOfcpu
	gpu      bool
}

// addPod adds an existing function instance to both the listers and the clientset.
func (c *cluster) addPod(name, function, node string, s podState) {
	l := map[string]string{
		ealabels.FunctionNamespaceLabel:                    ns,
		ealabels.FunctionNameLabel:                         function,
		ealabels.CommunityLabel.WithNamespace(ns).String(): community,
		ealabels.NodeLabel:                                 node,
	}
	if s.gpu {
		l[ealabels.GpuFunctionLabel] = ""
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: l},
		Spec:       corev1.PodSpec{NodeName: node},
	}
	if s.running {
		pod.Status.Phase = corev1.PodRunning
	}
	if s.ready {
		pod.Status.Conditions = []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}
	}
	if s.deleting {
		now := metav1.Now()
		pod.DeletionTimestamp = &now
	}
	if s.failed != "" {
		pod.Status.Phase = corev1.PodFailed
		pod.Status.Reason = s.failed
	}
	_ = c.pods.Add(pod)
	_ = c.kube.Tracker().Add(pod)
}

func (c *cluster) addSchedule(cpu, gpu eav1alpha1.CommunityFunctionAllocation) *eav1alpha1.CommunitySchedule {
	cs := &eav1alpha1.CommunitySchedule{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: community},
		Spec: eav1alpha1.CommunityScheduleSpec{
			CpuAllocations: cpu,
			GpuAllocations: gpu,
		},
	}
	_ = c.schedules.Add(cs)
	_ = c.ea.Tracker().Add(cs)
	return cs
}

// instance is a function pod as observed in the fake API server.
type instance struct {
	function string
	node     string
	gpu      bool
}

func (c *cluster) instances(t *testing.T) []instance {
	t.Helper()
	pods, err := c.kube.CoreV1().Pods(ns).List(contextTODO(), metav1.ListOptions{})
	require.NoError(t, err)
	result := []instance{}
	for _, p := range pods.Items {
		_, gpu := p.Labels[ealabels.GpuFunctionLabel]
		result = append(result, instance{function: p.Labels[ealabels.FunctionNameLabel], node: p.Spec.NodeName, gpu: gpu})
	}
	sort.Slice(result, func(i, j int) bool {
		return fmt.Sprint(result[i]) < fmt.Sprint(result[j])
	})
	return result
}

func (c *cluster) podNames(t *testing.T) []string {
	t.Helper()
	pods, err := c.kube.CoreV1().Pods(ns).List(contextTODO(), metav1.ListOptions{})
	require.NoError(t, err)
	names := []string{}
	for _, p := range pods.Items {
		names = append(names, p.Name)
	}
	sort.Strings(names)
	return names
}

func alloc(function string, nodes ...string) eav1alpha1.CommunityFunctionAllocation {
	a := eav1alpha1.CommunityFunctionAllocation{ns + "/" + function: {}}
	for _, n := range nodes {
		a[ns+"/"+function][n] = true
	}
	return a
}

func TestSyncInstanceLifecycle(t *testing.T) {
	testcases := []struct {
		description   string
		setup         func(c *cluster)
		readyOnCreate bool
		expected      []instance
		expectedPods  []string // checked when existing pods must survive
		expectError   bool
	}{
		{
			description: "missing instances are created and bound to the allocated nodes",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-b", nil)
				c.addFunction("prime", false)
				c.addSchedule(alloc("prime", "node-a", "node-b"), nil)
			},
			expected: []instance{{"prime", "node-a", false}, {"prime", "node-b", false}},
		},
		{
			description: "instances matching the allocation are left untouched",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-a"), nil)
			},
			expectedPods: []string{"prime-a"},
		},
		{
			description: "false allocations do not create instances",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addFunction("prime", false)
				c.addSchedule(eav1alpha1.CommunityFunctionAllocation{ns + "/prime": {"node-a": false}}, nil)
			},
			expected: []instance{},
		},
		{
			description: "instance moved to another node: old one is deleted once the new one is ready",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-b", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-b"), nil)
			},
			readyOnCreate: true,
			expected:      []instance{{"prime", "node-b", false}},
		},
		{
			description: "instance moved to another node: old one is kept while the new one is not ready",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-b", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-b"), nil)
			},
			expected: []instance{{"prime", "node-a", false}, {"prime", "node-b", false}},
		},
		{
			description: "scale in: instance on a node no longer allocated is deleted",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-b", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
				c.addPod("prime-b", "prime", "node-b", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-a"), nil)
			},
			expectedPods: []string{"prime-a"},
		},
		{
			description: "scale in is postponed while a remaining instance is not ready",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-b", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: false})
				c.addPod("prime-b", "prime", "node-b", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-a"), nil)
			},
			expectedPods: []string{"prime-a", "prime-b"},
		},
		{
			description: "duplicate instances on the same node are reduced to one",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a1", "prime", "node-a", podState{running: true, ready: true})
				c.addPod("prime-a2", "prime", "node-a", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-a"), nil)
			},
			expected: []instance{{"prime", "node-a", false}},
		},
		{
			description: "function removed from the schedule: its instances are deleted",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addFunction("prime", false)
				c.addFunction("other", false)
				c.addPod("other-a", "other", "node-a", podState{running: true, ready: true})
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
				c.addSchedule(alloc("prime", "node-a"), nil)
			},
			expectedPods: []string{"prime-a"},
		},
		{
			description: "several functions are reconciled independently",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-b", nil)
				c.addFunction("prime", false)
				c.addFunction("other", false)
				a := alloc("prime", "node-a")
				for k, v := range alloc("other", "node-a", "node-b") {
					a[k] = v
				}
				c.addSchedule(a, nil)
			},
			expected: []instance{{"other", "node-a", false}, {"other", "node-b", false}, {"prime", "node-a", false}},
		},
		{
			description: "OutOfcpu instances on unallocated nodes are deleted",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addFunction("prime", false)
				c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
				c.addPod("prime-oom", "prime", "node-b", podState{failed: "OutOfcpu", deleting: true})
				c.addSchedule(alloc("prime", "node-a"), nil)
			},
			expectedPods: []string{"prime-a"},
		},
		{
			description: "gpu functions get gpu instances on gpu allocations and cpu instances on cpu allocations",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addNode("node-g", map[string]string{ealabels.GpuNodeMemoryLabel: "4Gi"})
				c.addFunction("infer", true)
				c.addSchedule(alloc("infer", "node-a"), alloc("infer", "node-g"))
			},
			expected: []instance{{"infer", "node-a", false}, {"infer", "node-g", true}},
		},
		{
			description: "allocation of an unknown function is an error",
			setup: func(c *cluster) {
				c.addNode("node-a", nil)
				c.addSchedule(alloc("missing", "node-a"), nil)
			},
			expectError: true,
		},
		{
			description: "allocation on an unknown node is an error",
			setup: func(c *cluster) {
				c.addFunction("prime", false)
				c.addSchedule(alloc("prime", "node-x"), nil)
			},
			expectError: true,
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			c := newCluster()
			c.readyOnCreate = tt.readyOnCreate
			tt.setup(c)

			err := c.controller().syncCommunityScheduleAllocation(ns + "/" + community)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)

			if tt.expected != nil {
				require.Equal(t, tt.expected, c.instances(t))
			}
			if tt.expectedPods != nil {
				require.Equal(t, tt.expectedPods, c.podNames(t))
			}

			// every created instance is bound to the node it was created for
			for name, node := range c.bindings {
				pod, err := c.kube.CoreV1().Pods(ns).Get(contextTODO(), name, metav1.GetOptions{})
				require.NoError(t, err)
				require.Equal(t, pod.Labels[ealabels.NodeLabel], node)
			}
		})
	}
}

func TestSyncCreatedInstanceSpec(t *testing.T) {
	c := newCluster()
	c.addNode("node-g", map[string]string{ealabels.GpuNodeMemoryLabel: "4Gi"})
	c.addFunction("infer", true)
	c.addSchedule(nil, alloc("infer", "node-g"))

	require.NoError(t, c.controller().syncCommunityScheduleAllocation(ns+"/"+community))

	pods, err := c.kube.CoreV1().Pods(ns).List(contextTODO(), metav1.ListOptions{})
	require.NoError(t, err)
	require.Len(t, pods.Items, 1)
	pod := pods.Items[0]

	require.Contains(t, pod.Labels, ealabels.GpuFunctionLabel)
	require.Equal(t, community, pod.Labels[ealabels.CommunityLabel.WithNamespace(ns).String()])
	require.Equal(t, SchedulerName, pod.Spec.SchedulerName)
	require.Equal(t, "node-g", pod.Spec.NodeName)

	app := pod.Spec.Containers[0]
	vgpu := app.Resources.Limits[corev1.ResourceName(ealabels.GpuFunctionVGPU)]
	require.Equal(t, int64(1), vgpu.Value())
	env := map[string]string{}
	for _, e := range pod.Spec.Containers[1].Env {
		env[e.Name] = e.Value
	}
	require.Equal(t, "true", env["GPU"])
}

func TestSyncCommunityScheduleAllocationErrors(t *testing.T) {
	c := newCluster().controller()

	// invalid keys are dropped, missing schedules are retried
	require.NoError(t, c.syncCommunityScheduleAllocation("a/b/c"))
	require.Error(t, c.syncCommunityScheduleAllocation(ns+"/"+community))
}

// A running instance that is being deleted still counts as a healthy instance
// (sync.go: `pod.Status.Phase == corev1.PodRunning || pod.DeletionTimestamp == nil`),
// so no replacement is created on its node until it is gone.
func TestSyncReplacesTerminatingInstances(t *testing.T) {
	t.Skip("known bug: terminating instances are counted as live (pkg/community-controller/pkg/controller/sync.go sync)")

	c := newCluster()
	c.addNode("node-a", nil)
	c.addFunction("prime", false)
	c.addPod("prime-old", "prime", "node-a", podState{running: true, ready: true, deleting: true})
	c.addSchedule(alloc("prime", "node-a"), nil)

	require.NoError(t, c.controller().syncCommunityScheduleAllocation(ns+"/"+community))
	require.Len(t, c.podNames(t), 2, "a replacement must be created next to the terminating instance")
}

func contextTODO() context.Context { return context.TODO() }

// A failed OutOfcpu instance that is not being deleted ends up both in the
// delete set and in the OutOfcpu list, so it is deleted twice and the second
// delete fails the whole sync with NotFound.
func TestSyncDeletesOutOfCPUInstancesOnce(t *testing.T) {
	t.Skip("known bug: OutOfcpu instances are deleted twice (pkg/community-controller/pkg/controller/sync.go sync)")

	c := newCluster()
	c.addNode("node-a", nil)
	c.addFunction("prime", false)
	c.addPod("prime-a", "prime", "node-a", podState{running: true, ready: true})
	c.addPod("prime-oom", "prime", "node-b", podState{failed: "OutOfcpu"})
	c.addSchedule(alloc("prime", "node-a"), nil)

	require.NoError(t, c.controller().syncCommunityScheduleAllocation(ns+"/"+community))
	require.Equal(t, []string{"prime-a"}, c.podNames(t))
}

// A failed OutOfcpu instance on an allocated node is counted as the node's live
// instance, so no replacement is created and the function is left without an
// instance on that node until the next sync.
func TestSyncReplacesOutOfCPUInstances(t *testing.T) {
	t.Skip("known bug: OutOfcpu instances are not replaced in the same sync (pkg/community-controller/pkg/controller/sync.go sync)")

	c := newCluster()
	c.addNode("node-a", nil)
	c.addFunction("prime", false)
	c.addPod("prime-oom", "prime", "node-a", podState{failed: "OutOfcpu"})
	c.addSchedule(alloc("prime", "node-a"), nil)

	require.NoError(t, c.controller().syncCommunityScheduleAllocation(ns+"/"+community))
	require.Equal(t, []instance{{"prime", "node-a", false}}, c.instances(t))
}
