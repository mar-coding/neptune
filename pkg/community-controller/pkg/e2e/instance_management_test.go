package e2e_test

import (
	"context"
	"sort"
	"sync"
	"time"

	ealabels "github.com/lterrac/edge-autoscaler/pkg/labels"
	. "github.com/onsi/gomega"
	openfaasv1 "github.com/openfaas/faas-netes/pkg/apis/openfaas/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// instanceFunctionName is a function whose instances become ready (the image
// serves /health, which the http-metrics sidecar probes).
const instanceFunctionName = "prime-numbers"

var (
	overrideMu         sync.Mutex
	allocationOverride map[string]map[string]bool
)

// setAllocationOverride makes the fake solver return the given CPU allocation.
// nil restores the default (every function on every node).
func setAllocationOverride(a map[string]map[string]bool) {
	overrideMu.Lock()
	defer overrideMu.Unlock()
	allocationOverride = a
}

func getAllocationOverride() map[string]map[string]bool {
	overrideMu.Lock()
	defer overrideMu.Unlock()
	return allocationOverride
}

func instanceFunction() *openfaasv1.Function {
	return &openfaasv1.Function{
		ObjectMeta: metav1.ObjectMeta{Name: instanceFunctionName, Namespace: namespace},
		Spec: openfaasv1.FunctionSpec{
			Name:  instanceFunctionName,
			Image: "systemautoscaler/prime-numbers:dev",
			// NewSchedulingInput requires labels to be set
			Labels:   &map[string]string{"edgeautoscaler.polimi.it/scheduler": "edge-autoscaler"},
			Requests: &openfaasv1.FunctionResources{Memory: "10Mi"},
		},
	}
}

// allocateOn returns an allocation of the instance function on the given nodes.
func allocateOn(nodes ...string) map[string]map[string]bool {
	a := map[string]map[string]bool{namespace + "/" + instanceFunctionName: {}}
	for _, n := range nodes {
		a[namespace+"/"+instanceFunctionName][n] = true
	}
	return a
}

// triggerRescheduling touches the function: the community controller reschedules
// on function updates.
func triggerRescheduling(ctx context.Context) {
	f, err := openfaasClient.OpenfaasV1().Functions(namespace).Get(ctx, instanceFunctionName, metav1.GetOptions{})
	Expect(err).ToNot(HaveOccurred())
	if f.Annotations == nil {
		f.Annotations = map[string]string{}
	}
	f.Annotations["e2e/touched"] = time.Now().Format(time.RFC3339Nano)
	_, err = openfaasClient.OpenfaasV1().Functions(namespace).Update(ctx, f, metav1.UpdateOptions{})
	Expect(err).ToNot(HaveOccurred())
}

// liveInstances returns the instance function pods that are not being deleted, by node.
func liveInstances(ctx context.Context) map[string][]corev1.Pod {
	pods, err := kubeClient.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: ealabels.FunctionNameLabel + "=" + instanceFunctionName,
	})
	Expect(err).ToNot(HaveOccurred())
	result := map[string][]corev1.Pod{}
	for _, p := range pods.Items {
		if p.DeletionTimestamp == nil {
			result[p.Spec.NodeName] = append(result[p.Spec.NodeName], p)
		}
	}
	return result
}

// instanceNodes returns the sorted nodes running exactly one live instance, or
// nil when any node runs more than one.
func instanceNodes(ctx context.Context) []string {
	nodes := []string{}
	for node, pods := range liveInstances(ctx) {
		if len(pods) != 1 {
			return nil
		}
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	return nodes
}

func isReady(p corev1.Pod) bool {
	for _, c := range p.Status.Conditions {
		if c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

// allInstancesReady reports whether every live instance is ready.
func allInstancesReady(ctx context.Context) bool {
	for _, pods := range liveInstances(ctx) {
		for _, p := range pods {
			if !isReady(p) {
				return false
			}
		}
	}
	return true
}

// setCommunity sets the community label of the given worker nodes and returns
// the previous values, to be restored with the same function.
func setCommunity(ctx context.Context, communities map[string]string) map[string]string {
	previous := map[string]string{}
	label := ealabels.CommunityLabel.WithNamespace(namespace).String()
	for name, community := range communities {
		node, err := kubeClient.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		Expect(err).ToNot(HaveOccurred())
		previous[name] = node.Labels[label]
		node.Labels[label] = community
		_, err = kubeClient.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
		Expect(err).ToNot(HaveOccurred())
	}
	return previous
}
