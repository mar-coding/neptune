package queue

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/workqueue"
)

// newTestQueue returns a queue whose rate limiter never delays items,
// so tests don't depend on wall-clock backoff.
func newTestQueue() Queue {
	return NewQueue("test", workqueue.NewItemExponentialFailureRateLimiter(0, 0))
}

func newPod(namespace, name string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
		},
	}
}

func TestNewQueueDefaultRateLimiter(t *testing.T) {
	q := NewQueue("default", nil)
	defer q.ShutDown()
	require.NotNil(t, q.queue)
}

func TestEnqueue(t *testing.T) {
	testcases := []struct {
		description string
		obj         interface{}
		expectedLen int
		expectedKey string
	}{
		{
			description: "namespaced object is enqueued as namespace/name",
			obj:         newPod("ns", "pod"),
			expectedLen: 1,
			expectedKey: "ns/pod",
		},
		{
			description: "cluster scoped object is enqueued as name",
			obj:         &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "node"}},
			expectedLen: 1,
			expectedKey: "node",
		},
		{
			description: "object without metadata is discarded",
			obj:         struct{}{},
			expectedLen: 0,
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			q := newTestQueue()
			defer q.ShutDown()

			q.Enqueue(tt.obj)

			require.Equal(t, tt.expectedLen, q.queue.Len())
			if tt.expectedLen > 0 {
				item, _ := q.queue.Get()
				require.Equal(t, tt.expectedKey, item)
			}
		})
	}
}

func TestEventHandlers(t *testing.T) {
	testcases := []struct {
		description string
		handle      func(q *Queue)
		expectedKey string
	}{
		{
			description: "add enqueues the new object",
			handle:      func(q *Queue) { q.Add(newPod("ns", "added")) },
			expectedKey: "ns/added",
		},
		{
			description: "update enqueues the new object",
			handle:      func(q *Queue) { q.Update(newPod("ns", "old"), newPod("ns", "new")) },
			expectedKey: "ns/new",
		},
		{
			description: "deletion enqueues the old object",
			handle:      func(q *Queue) { q.Deletion(newPod("ns", "deleted")) },
			expectedKey: "ns/deleted",
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			q := newTestQueue()
			defer q.ShutDown()

			tt.handle(&q)

			require.Equal(t, 1, q.queue.Len())
			item, _ := q.queue.Get()
			require.Equal(t, tt.expectedKey, item)
		})
	}
}

func TestProcessNextItem(t *testing.T) {
	testcases := []struct {
		description      string
		item             interface{}
		syncErr          error
		expectSyncCalled bool
		expectRequeued   bool
	}{
		{
			description:      "successful sync forgets the item",
			item:             "ns/name",
			expectSyncCalled: true,
			expectRequeued:   false,
		},
		{
			description:      "failed sync requeues the item",
			item:             "ns/name",
			syncErr:          fmt.Errorf("transient error"),
			expectSyncCalled: true,
			expectRequeued:   true,
		},
		{
			description:      "non string item is dropped without calling sync",
			item:             42,
			expectSyncCalled: false,
			expectRequeued:   false,
		},
	}

	for _, tt := range testcases {
		t.Run(tt.description, func(t *testing.T) {
			q := newTestQueue()
			defer q.ShutDown()

			q.queue.Add(tt.item)

			var syncedKey string
			called := false
			sync := func(key string) error {
				called = true
				syncedKey = key
				return tt.syncErr
			}

			require.True(t, q.ProcessNextItem(sync))
			require.Equal(t, tt.expectSyncCalled, called)
			if tt.expectSyncCalled {
				require.Equal(t, tt.item, syncedKey)
			}

			if tt.expectRequeued {
				require.Equal(t, 1, q.queue.NumRequeues(tt.item))
				require.Equal(t, 1, q.queue.Len())
			} else {
				require.Equal(t, 0, q.queue.NumRequeues(tt.item))
				require.Equal(t, 0, q.queue.Len())
			}
		})
	}
}

func TestProcessNextItemAfterShutdown(t *testing.T) {
	q := newTestQueue()
	q.ShutDown()

	require.False(t, q.ProcessNextItem(func(key string) error {
		t.Fatalf("sync must not be called after shutdown")
		return nil
	}))
}
