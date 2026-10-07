package scraper

import (
	"fmt"
	"testing"

	cc "github.com/lterrac/edge-autoscaler/pkg/community-controller/pkg/controller"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/lterrac/edge-autoscaler/pkg/metrics"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	metricsv1beta1 "k8s.io/metrics/pkg/apis/metrics/v1beta1"
	metricsfake "k8s.io/metrics/pkg/client/clientset/versioned/fake"
)

const namespace = "openfaas-fn"

func cpu(milli int64) corev1.ResourceList {
	return corev1.ResourceList{corev1.ResourceCPU: *resource.NewMilliQuantity(milli, resource.DecimalSI)}
}

func container(name string, request, limit int64) corev1.Container {
	c := corev1.Container{Name: name}
	if request > 0 {
		c.Resources.Requests = cpu(request)
	}
	if limit > 0 {
		c.Resources.Limits = cpu(limit)
	}
	return c
}

func newPod(name, node string, l map[string]string, containers ...corev1.Container) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: l},
		Spec:       corev1.PodSpec{NodeName: node, Containers: containers},
	}
}

func newPodMetrics(name string, usage map[string]int64) *metricsv1beta1.PodMetrics {
	m := &metricsv1beta1.PodMetrics{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name}}
	for c, u := range usage {
		m.Containers = append(m.Containers, metricsv1beta1.ContainerMetrics{Name: c, Usage: cpu(u)})
	}
	return m
}

func newScraper(pods []*corev1.Pod, podErr error, podMetrics ...runtime.Object) (*defaultScraper, chan metrics.RawResourceData) {
	// The fake clientset would register PodMetrics under "podmetricses", while
	// PodMetricses().Get looks them up as "pods": add them with the explicit resource.
	client := metricsfake.NewSimpleClientset()
	gvr := metricsv1beta1.SchemeGroupVersion.WithResource("pods")
	for _, m := range podMetrics {
		if err := client.Tracker().Create(gvr, m, namespace); err != nil {
			panic(err)
		}
	}

	resourceChan := make(chan metrics.RawResourceData, 100)
	return &defaultScraper{
		pods: func(selector labels.Selector) ([]*corev1.Pod, error) {
			return pods, podErr
		},
		metrics:      client.MetricsV1beta1(),
		resourceChan: resourceChan,
	}, resourceChan
}

func drain(ch chan metrics.RawResourceData) map[string]metrics.RawResourceData {
	result := make(map[string]metrics.RawResourceData)
	for {
		select {
		case d := <-ch:
			result[d.Pod] = d
		default:
			return result
		}
	}
}

func TestScrape(t *testing.T) {
	communityLabel := ealabels.CommunityLabel.WithNamespace(namespace).String()

	testcases := []struct {
		description string
		pods        []*corev1.Pod
		podMetrics  []runtime.Object
		expected    map[string]metrics.RawResourceData
	}{
		{
			description: "function pod: usage and resources are summed, http-metrics sidecar is ignored",
			pods: []*corev1.Pod{
				newPod("prime-1", "node-a",
					map[string]string{ealabels.FunctionNameLabel: "prime", communityLabel: "community-1"},
					container("prime", 200, 400),
					container("worker", 100, 100),
					container(cc.HttpMetrics, 50, 50),
				),
			},
			podMetrics: []runtime.Object{
				newPodMetrics("prime-1", map[string]int64{"prime": 150, "worker": 30, cc.HttpMetrics: 999}),
			},
			expected: map[string]metrics.RawResourceData{
				"prime-1": {
					Node: "node-a", Function: "prime", Pod: "prime-1", Namespace: namespace,
					Community: "community-1", Cores: 180, Requests: 300, Limits: 500,
				},
			},
		},
		{
			description: "containers without both request and limit only count usage",
			pods: []*corev1.Pod{
				newPod("plain", "node-a", nil, container("main", 100, 0)),
			},
			podMetrics: []runtime.Object{newPodMetrics("plain", map[string]int64{"main": 70})},
			expected: map[string]metrics.RawResourceData{
				"plain": {Node: "node-a", Pod: "plain", Namespace: namespace, Cores: 70},
			},
		},
		{
			description: "unscheduled pods and pods without metrics are skipped",
			pods: []*corev1.Pod{
				newPod("pending", "", nil, container("main", 100, 100)),
				newPod("no-metrics", "node-a", nil, container("main", 100, 100)),
				newPod("ok", "node-b", nil, container("main", 100, 100)),
			},
			podMetrics: []runtime.Object{
				newPodMetrics("pending", map[string]int64{"main": 10}),
				newPodMetrics("ok", map[string]int64{"main": 20}),
			},
			expected: map[string]metrics.RawResourceData{
				"ok": {Node: "node-b", Pod: "ok", Namespace: namespace, Cores: 20, Requests: 100, Limits: 100},
			},
		},
		{
			description: "totals are reset between pods",
			pods: []*corev1.Pod{
				newPod("first", "node-a", nil, container("main", 100, 200)),
				newPod("second", "node-a", nil, container("main", 300, 400)),
			},
			podMetrics: []runtime.Object{
				newPodMetrics("first", map[string]int64{"main": 10}),
				newPodMetrics("second", map[string]int64{"main": 20}),
			},
			expected: map[string]metrics.RawResourceData{
				"first":  {Node: "node-a", Pod: "first", Namespace: namespace, Cores: 10, Requests: 100, Limits: 200},
				"second": {Node: "node-a", Pod: "second", Namespace: namespace, Cores: 20, Requests: 300, Limits: 400},
			},
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			s, ch := newScraper(tt.pods, nil, tt.podMetrics...)

			s.scrape()

			result := drain(ch)
			require.Len(t, result, len(tt.expected))
			for pod, expected := range tt.expected {
				actual, ok := result[pod]
				require.True(t, ok, "missing data for pod %s", pod)
				require.False(t, actual.Timestamp.IsZero())
				actual.Timestamp = expected.Timestamp
				require.Equal(t, expected, actual)
			}
		})
	}
}

func TestScrapePodListError(t *testing.T) {
	s, ch := newScraper(nil, fmt.Errorf("lister unavailable"))

	s.scrape()

	require.Empty(t, drain(ch))
}
