//go:build integration
// +build integration

package persistor

import (
	"context"
	"testing"
	"time"

	"github.com/lterrac/edge-autoscaler/pkg/db/dbtest"
	"github.com/lterrac/edge-autoscaler/pkg/metrics"
	"github.com/stretchr/testify/require"
)

func TestResourcePersistor(t *testing.T) {
	opts := dbtest.Options(t)
	pool := dbtest.Pool(t, opts, table)

	resourceChan := make(chan metrics.RawResourceData, 100)
	p := NewResourcePersistor(opts, resourceChan)
	require.NoError(t, p.SetupDBConnection())

	done := make(chan struct{})
	go func() {
		p.Persist()
		close(done)
	}()

	now := time.Now().UTC().Truncate(time.Millisecond)
	const samples = 25
	for i := 0; i < samples; i++ {
		resourceChan <- metrics.RawResourceData{
			Timestamp: now.Add(time.Duration(i) * time.Millisecond),
			Node:      "node-a",
			Function:  "prime-numbers",
			Pod:       "prime-numbers-abc",
			Namespace: "openfaas-fn",
			Community: "community-1",
			Cores:     int64(100 + i),
			Requests:  200,
			Limits:    400,
		}
	}

	// closing the channel flushes the data and stops the persistor
	close(resourceChan)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("persistor did not stop after the channel was closed")
	}

	var count, minCores, maxCores, requests, limits int64
	err := pool.QueryRow(context.Background(),
		"SELECT count(*), min(cores), max(cores), min(requests), max(limits) FROM resource WHERE node='node-a' AND function='prime-numbers' AND namespace='openfaas-fn' AND community='community-1' AND pod='prime-numbers-abc'",
	).Scan(&count, &minCores, &maxCores, &requests, &limits)
	require.NoError(t, err)
	require.Equal(t, int64(samples), count)
	require.Equal(t, int64(100), minCores)
	require.Equal(t, int64(100+samples-1), maxCores)
	require.Equal(t, int64(200), requests)
	require.Equal(t, int64(400), limits)
}
