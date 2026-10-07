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

func TestMetricsPersistor(t *testing.T) {
	opts := dbtest.Options(t)
	pool := dbtest.Pool(t, opts, table)

	metricChan := make(chan metrics.RawResponseTime, 100)
	p := NewMetricsPersistor(opts, metricChan)
	require.NoError(t, p.SetupDBConnection())

	done := make(chan struct{})
	go func() {
		p.PollMetrics()
		close(done)
	}()

	now := time.Now().UTC().Truncate(time.Millisecond)
	sent := []metrics.RawResponseTime{
		{Source: "node-a", Destination: "node-b", Latency: 30, StatusCode: 200, Gpu: false, Path: "/prime/1000", Method: "GET"},
		{Source: "node-a", Destination: "node-a", Latency: 5, StatusCode: 200, Gpu: true, Path: "/prime/10", Method: "GET"},
		{Source: "node-b", Destination: "node-b", Latency: 900, StatusCode: 502, Description: "backend unreachable", Path: "/prime/1", Method: "POST"},
	}
	for i, m := range sent {
		m.Timestamp = now.Add(time.Duration(i) * time.Millisecond)
		m.Function = "prime-numbers"
		m.Namespace = "openfaas-fn"
		m.Community = "community-1"
		metricChan <- m
	}

	// closing the channel flushes the data and stops the persistor
	close(metricChan)
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("persistor did not stop after the channel was closed")
	}

	rows, err := pool.Query(context.Background(),
		"SELECT source, destination, gpu, latency, status, description, path, method FROM metric WHERE function='prime-numbers' AND namespace='openfaas-fn' AND community='community-1' ORDER BY timestamp")
	require.NoError(t, err)
	defer rows.Close()

	var stored []metrics.RawResponseTime
	for rows.Next() {
		var m metrics.RawResponseTime
		require.NoError(t, rows.Scan(&m.Source, &m.Destination, &m.Gpu, &m.Latency, &m.StatusCode, &m.Description, &m.Path, &m.Method))
		stored = append(stored, m)
	}
	require.NoError(t, rows.Err())

	require.Len(t, stored, len(sent))
	for i := range sent {
		expected := sent[i]
		expected.Timestamp, expected.Function, expected.Namespace, expected.Community = time.Time{}, "", "", ""
		require.Equal(t, expected, stored[i])
	}
}
