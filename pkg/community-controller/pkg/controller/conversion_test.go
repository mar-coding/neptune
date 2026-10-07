package controller

import (
	"testing"

	eav1alpha1 "github.com/lterrac/edge-autoscaler/pkg/apis/edgeautoscaler/v1alpha1"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/api/resource"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestToCommunitySchedule(t *testing.T) {
	original := &eav1alpha1.CommunitySchedule{
		ObjectMeta: v1.ObjectMeta{Namespace: "openfaas-fn", Name: "community-1", ResourceVersion: "42"},
		Spec: eav1alpha1.CommunityScheduleSpec{
			AlgorithmService: "http://solver:5000",
			CpuAllocations:   eav1alpha1.CommunityFunctionAllocation{"old/fn": {"node-z": true}},
		},
	}

	output := &SchedulingOutput{
		CpuRoutingRules: map[string]map[string]map[string]float64{
			"node-a": {"openfaas-fn/prime": {"node-a": 0.25, "node-b": 0.75}},
			"node-b": {"openfaas-fn/prime": {"node-b": 1}},
		},
		CpuAllocations: map[string]map[string]bool{
			"openfaas-fn/prime": {"node-a": true, "node-b": true, "node-c": false},
		},
		GpuRoutingRules: map[string]map[string]map[string]float64{
			"node-a": {"openfaas-fn/infer": {"node-g": 0.5}},
		},
		GpuAllocations: map[string]map[string]bool{
			"openfaas-fn/infer": {"node-g": true},
		},
	}

	cs := output.ToCommunitySchedule(original)

	t.Run("routing weights are stored as milli quantities", func(t *testing.T) {
		require.Equal(t, eav1alpha1.CommunitySourceRoutingRule{
			"node-a": {"openfaas-fn/prime": {"node-a": resource.MustParse("250m"), "node-b": resource.MustParse("750m")}},
			"node-b": {"openfaas-fn/prime": {"node-b": resource.MustParse("1")}},
		}, normalize(cs.Spec.CpuRoutingRules))
		require.Equal(t, eav1alpha1.CommunitySourceRoutingRule{
			"node-a": {"openfaas-fn/infer": {"node-g": resource.MustParse("500m")}},
		}, normalize(cs.Spec.GpuRoutingRules))
	})

	t.Run("only true allocations are kept and old ones are replaced", func(t *testing.T) {
		require.Equal(t, eav1alpha1.CommunityFunctionAllocation{
			"openfaas-fn/prime": {"node-a": true, "node-b": true},
		}, cs.Spec.CpuAllocations)
		require.Equal(t, eav1alpha1.CommunityFunctionAllocation{
			"openfaas-fn/infer": {"node-g": true},
		}, cs.Spec.GpuAllocations)
	})

	t.Run("metadata and other fields are preserved, input is not mutated", func(t *testing.T) {
		require.Equal(t, original.ObjectMeta, cs.ObjectMeta)
		require.Equal(t, "http://solver:5000", cs.Spec.AlgorithmService)
		require.Equal(t, eav1alpha1.CommunityFunctionAllocation{"old/fn": {"node-z": true}}, original.Spec.CpuAllocations)
		require.Nil(t, original.Spec.CpuRoutingRules)
	})
}

func TestToCommunityScheduleEmptyOutput(t *testing.T) {
	cs := (&SchedulingOutput{}).ToCommunitySchedule(&eav1alpha1.CommunitySchedule{})

	require.Empty(t, cs.Spec.CpuRoutingRules)
	require.Empty(t, cs.Spec.GpuRoutingRules)
	require.Empty(t, cs.Spec.CpuAllocations)
	require.Empty(t, cs.Spec.GpuAllocations)
}

// normalize re-parses quantities so that equal values compare equal regardless
// of their internal representation.
func normalize(rules eav1alpha1.CommunitySourceRoutingRule) eav1alpha1.CommunitySourceRoutingRule {
	out := eav1alpha1.CommunitySourceRoutingRule{}
	for source, functions := range rules {
		out[source] = eav1alpha1.CommunityFunctionRoutingRule{}
		for function, destinations := range functions {
			out[source][function] = eav1alpha1.CommunityDestinationRoutingRule{}
			for destination, q := range destinations {
				out[source][function][destination] = resource.MustParse(q.String())
			}
		}
	}
	return out
}
