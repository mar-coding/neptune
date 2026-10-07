package labels

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCommunityWithNamespace(t *testing.T) {
	testcases := []struct {
		description string
		label       Community
		namespace   string
		expected    string
	}{
		{
			description: "community label scoped to a namespace",
			label:       CommunityLabel,
			namespace:   "openfaas-fn",
			expected:    "edgeautoscaler.polimi.it/openfaas-fn.community",
		},
		{
			description: "custom community label",
			label:       Community("example.com/zone"),
			namespace:   "e2e",
			expected:    "example.com/e2e.zone",
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.label.WithNamespace(tt.namespace).String())
		})
	}
}

func TestCommunityRoleWithNamespace(t *testing.T) {
	role := CommunityRole("edgeautoscaler.polimi.it/role")
	require.Equal(t, "edgeautoscaler.polimi.it/e2e.role", role.WithNamespace("e2e").String())
}

func TestCommunityInstances(t *testing.T) {
	testcases := []struct {
		description string
		build       func() CommunityInstances
		expected    string
	}{
		{
			description: "namespace only",
			build:       func() CommunityInstances { return CommunityInstancesLabel.WithNamespace("openfaas-fn") },
			expected:    "edgeautoscaler.polimi.it/openfaas-fn.{name}.instances",
		},
		{
			description: "name only",
			build:       func() CommunityInstances { return CommunityInstancesLabel.WithName("prime-numbers") },
			expected:    "edgeautoscaler.polimi.it/{namespace}.prime-numbers.instances",
		},
		{
			description: "namespace then name",
			build: func() CommunityInstances {
				return CommunityInstancesLabel.WithNamespace("openfaas-fn").WithName("prime-numbers")
			},
			expected: "edgeautoscaler.polimi.it/openfaas-fn.prime-numbers.instances",
		},
		{
			description: "name then namespace",
			build: func() CommunityInstances {
				return CommunityInstancesLabel.WithName("prime-numbers").WithNamespace("openfaas-fn")
			},
			expected: "edgeautoscaler.polimi.it/openfaas-fn.prime-numbers.instances",
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			require.Equal(t, tt.expected, tt.build().String())
		})
	}
}
