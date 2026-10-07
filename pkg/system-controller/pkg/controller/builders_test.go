package controller

import (
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var communityConfiguration = &eav1alpha1.CommunityConfiguration{
	ObjectMeta: metav1.ObjectMeta{Namespace: "openfaas-fn", Name: "example-cc", UID: "cc-uid"},
}

func requireOwnedByConfiguration(t *testing.T, refs []metav1.OwnerReference) {
	t.Helper()
	require.Len(t, refs, 1)
	require.Equal(t, "CommunityConfiguration", refs[0].Kind)
	require.Equal(t, "edgeautoscaler.polimi.it/v1alpha1", refs[0].APIVersion)
	require.Equal(t, "example-cc", refs[0].Name)
	require.Equal(t, communityConfiguration.UID, refs[0].UID)
	require.True(t, *refs[0].Controller)
}

func TestNewCommunitySchedule(t *testing.T) {
	cs := NewCommunitySchedule("openfaas-fn", "community-1", communityConfiguration)

	require.Equal(t, "openfaas-fn", cs.Namespace)
	require.Equal(t, "community-1", cs.Name)
	requireOwnedByConfiguration(t, cs.OwnerReferences)

	require.Equal(t, "http://allocation-algorithm.default.svc.cluster.local:5000", cs.Spec.AlgorithmService)
	require.NotNil(t, cs.Spec.CpuRoutingRules)
	require.NotNil(t, cs.Spec.GpuRoutingRules)
	require.NotNil(t, cs.Spec.CpuAllocations)
	require.NotNil(t, cs.Spec.GpuAllocations)
	require.Empty(t, cs.Spec.CpuAllocations)
}

func TestNewCommunityController(t *testing.T) {
	dp := NewCommunityController("openfaas-fn", "community-1", communityConfiguration)

	require.Equal(t, "openfaas-fn", dp.Namespace)
	require.Equal(t, "community-1", dp.Name)
	require.Contains(t, dp.Labels, ealabels.CommunityControllerDeploymentLabel)
	requireOwnedByConfiguration(t, dp.OwnerReferences)

	require.Equal(t, int32(1), *dp.Spec.Replicas)
	require.Equal(t, dp.Spec.Selector.MatchLabels, dp.Spec.Template.Labels)
	require.Equal(t, map[string]string{"community": "community-1", "app": "community-controller"}, dp.Spec.Template.Labels)

	pod := dp.Spec.Template.Spec
	require.Equal(t, map[string]string{ealabels.MasterNodeLabel: "true"}, pod.NodeSelector)
	require.Equal(t, "community-controller", pod.ServiceAccountName)
	require.Len(t, pod.Containers, 1)

	c := pod.Containers[0]
	require.Equal(t, "systemautoscaler/community-controller:dev", c.Image)
	require.ElementsMatch(t, []corev1.EnvVar{
		{Name: "COMMUNITY_NAMESPACE", Value: "openfaas-fn"},
		{Name: "COMMUNITY_NAME", Value: "community-1"},
	}, c.Env)
	require.Equal(t, c.Resources.Requests, c.Resources.Limits)
}
