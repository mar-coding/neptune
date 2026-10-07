//go:build integration
// +build integration

package delayclient

import (
	"context"
	"testing"
	"time"

	"github.com/lterrac/edge-autoscaler/pkg/db/dbtest"
	"github.com/stretchr/testify/require"
)

func TestSQLDelayClientGetDelays(t *testing.T) {
	opts := dbtest.Options(t)
	pool := dbtest.Pool(t, opts, "ping")

	now := time.Now().UTC().Truncate(time.Millisecond)
	rows := []struct {
		at       time.Time
		from, to string
		latency  float64
	}{
		// older measurements must be ignored in favour of the latest one per pair
		{now.Add(-2 * time.Minute), "node-a", "node-b", 50},
		{now.Add(-1 * time.Minute), "node-a", "node-b", 40},
		{now, "node-a", "node-b", 10},
		{now.Add(-1 * time.Minute), "node-b", "node-a", 12},
		// (timestamp, from_node) is the primary key: same source needs distinct timestamps
		{now.Add(-1 * time.Second), "node-a", "node-a", 1},
	}
	for _, r := range rows {
		_, err := pool.Exec(context.Background(),
			"INSERT INTO ping (timestamp, from_node, to_node, avg_latency, max_latency, min_latency) VALUES ($1, $2, $3, $4, $4, $4)",
			r.at, r.from, r.to, r.latency)
		require.NoError(t, err)
	}

	client := NewSQLDelayClient(opts)
	require.NoError(t, client.SetupDBConnection())
	defer client.Stop()

	delays, err := client.GetDelays()
	require.NoError(t, err)

	actual := map[[2]string]float64{}
	for _, d := range delays {
		actual[[2]string{d.FromNode, d.ToNode}] = d.Latency
	}
	require.Equal(t, map[[2]string]float64{
		{"node-a", "node-b"}: 10,
		{"node-b", "node-a"}: 12,
		{"node-a", "node-a"}: 1,
	}, actual)
}

func TestSQLDelayClientEmptyTable(t *testing.T) {
	opts := dbtest.Options(t)
	dbtest.Pool(t, opts, "ping")

	client := NewSQLDelayClient(opts)
	require.NoError(t, client.SetupDBConnection())
	defer client.Stop()

	delays, err := client.GetDelays()
	require.NoError(t, err)
	require.Empty(t, delays)
}
