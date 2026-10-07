package controller

import (
	"fmt"
	"strings"
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	openfaasv1 "github.com/openfaas/faas-netes/pkg/apis/openfaas/v1"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func newTestFunction(limits, requests *openfaasv1.FunctionResources) *openfaasv1.Function {
	return &openfaasv1.Function{
		ObjectMeta: v1.ObjectMeta{Namespace: "openfaas-fn", Name: "prime", UID: "uid-1", Labels: map[string]string{}},
		Spec: openfaasv1.FunctionSpec{
			Name:     "prime",
			Image:    "systemautoscaler/prime-numbers:dev",
			Limits:   limits,
			Requests: requests,
		},
	}
}

func res(cpu, memory string) *openfaasv1.FunctionResources {
	return &openfaasv1.FunctionResources{CPU: cpu, Memory: memory}
}

func TestMakeResources(t *testing.T) {
	testcases := []struct {
		description string
		limits      *openfaasv1.FunctionResources
		requests    *openfaasv1.FunctionResources
		expectError bool
	}{
		{description: "equal requests and limits", limits: res("200m", "1000Mi"), requests: res("200m", "1000Mi")},
		{description: "different memory", limits: res("200m", "2000Mi"), requests: res("200m", "1000Mi"), expectError: true},
		{description: "different cpu", limits: res("400m", "1000Mi"), requests: res("200m", "1000Mi"), expectError: true},
		{description: "missing memory", limits: res("200m", ""), requests: res("200m", ""), expectError: true},
		{description: "missing cpu", limits: res("", "1000Mi"), requests: res("", "1000Mi"), expectError: true},
		{description: "missing limits", requests: res("200m", "1000Mi"), expectError: true},
		{description: "invalid quantity", limits: res("lots", "1000Mi"), requests: res("lots", "1000Mi"), expectError: true},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			r, err := makeResources(newTestFunction(tt.limits, tt.requests))
			require.NotNil(t, r)
			if tt.expectError {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			for _, list := range []corev1.ResourceList{r.Requests, r.Limits} {
				require.True(t, resource.MustParse("200m").Equal(list[corev1.ResourceCPU]))
				require.True(t, resource.MustParse("1000Mi").Equal(list[corev1.ResourceMemory]))
			}
		})
	}
}

func TestMakeEnvVars(t *testing.T) {
	f := newTestFunction(nil, nil)
	require.Empty(t, makeEnvVars(f))

	f.Spec.Handler = "python index.py"
	f.Spec.Environment = &map[string]string{"MODE": "fast"}
	require.ElementsMatch(t, []corev1.EnvVar{
		{Name: "fprocess", Value: "python index.py"},
		{Name: "MODE", Value: "fast"},
	}, makeEnvVars(f))
}

func TestHash(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		h := hash(8)
		require.Len(t, h, 8)
		require.Equal(t, strings.ToLower(h), h)
		for _, r := range h {
			require.Contains(t, string(letters), string(r))
		}
		seen[h] = true
	}
	require.Greater(t, len(seen), 90, "hash should rarely collide")
}

func TestNewCPUPod(t *testing.T) {
	cs := &eav1alpha1.CommunitySchedule{ObjectMeta: v1.ObjectMeta{Namespace: "openfaas-fn", Name: "community-1"}}
	node := &corev1.Node{ObjectMeta: v1.ObjectMeta{Name: "node-a"}}

	testcases := []struct {
		description   string
		gpuFunction   bool
		image         string
		expectedImage string
	}{
		{description: "cpu function", image: "repo/prime:dev", expectedImage: "repo/prime:dev"},
		{description: "gpu function replica on cpu uses the cpu image", gpuFunction: true, image: "repo/infer-gpu:dev", expectedImage: "repo/infer:dev"},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			f := newTestFunction(res("200m", "1000Mi"), res("200m", "1000Mi"))
			f.Spec.Image = tt.image
			if tt.gpuFunction {
				f.Labels[ealabels.GpuFunctionLabel] = ""
			}

			pod := newCPUPod(f, cs, node)

			require.True(t, strings.HasPrefix(pod.Name, "prime-"))
			require.Len(t, pod.Name, len("prime-")+8)
			require.Equal(t, "openfaas-fn", pod.Namespace)
			require.Equal(t, map[string]string{
				ealabels.FunctionNamespaceLabel:                               "openfaas-fn",
				ealabels.FunctionNameLabel:                                    "prime",
				ealabels.CommunityLabel.WithNamespace("openfaas-fn").String(): "community-1",
				ealabels.NodeLabel:                                            "node-a",
				"autoscaling":                                                 "vertical",
			}, pod.Labels)

			require.Len(t, pod.OwnerReferences, 1)
			require.Equal(t, "Function", pod.OwnerReferences[0].Kind)
			require.Equal(t, f.UID, pod.OwnerReferences[0].UID)
			require.Equal(t, SchedulerName, pod.Spec.SchedulerName)

			require.Len(t, pod.Spec.Containers, 2)
			app, sidecar := pod.Spec.Containers[0], pod.Spec.Containers[1]
			require.Equal(t, tt.expectedImage, app.Image)
			require.True(t, resource.MustParse("200m").Equal(app.Resources.Limits[corev1.ResourceCPU]))

			require.Equal(t, HttpMetrics, sidecar.Name)
			require.Equal(t, fmt.Sprintf("%s:%s", HttpMetricsImage, HttpMetricsVersion), sidecar.Image)
			require.Equal(t, int32(8000), sidecar.Ports[0].ContainerPort)
			env := map[string]string{}
			for _, e := range sidecar.Env {
				env[e.Name] = e.Value
			}
			require.Equal(t, "node-a", env["NODE"])
			require.Equal(t, "prime", env["FUNCTION"])
			require.Equal(t, "openfaas-fn", env["NAMESPACE"])
			require.Equal(t, "community-1", env["COMMUNITY"])
			require.Equal(t, "false", env["GPU"])
		})
	}
}
