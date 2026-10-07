// Package dbtest provides helpers for integration tests that need the metrics
// database. Tests are compiled only with the "integration" build tag and are run
// by hack/test-integration.sh, which starts TimescaleDB from config/metric-db.
package dbtest

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v4/pgxpool"
	"github.com/lterrac/edge-autoscaler/pkg/db"
)

const (
	// HostEnv is the database host, in the format expected by db.Options (e.g. postgresql://localhost).
	HostEnv = "NEPTUNE_TEST_DB_HOST"
	// PortEnv is the database port. Tests are skipped when it is not set.
	PortEnv = "NEPTUNE_TEST_DB_PORT"
)

// Options returns the database options for the test database, skipping the test
// when no database is configured.
func Options(t *testing.T) db.Options {
	t.Helper()

	port := os.Getenv(PortEnv)
	if port == "" {
		t.Skipf("%s not set: run hack/test-integration.sh", PortEnv)
	}

	host := os.Getenv(HostEnv)
	if host == "" {
		host = "postgresql://localhost"
	}

	return db.Options{Host: host, Port: port, User: "user", Pass: "password", DB: "user"}
}

// Pool connects to the test database and empties the given tables.
func Pool(t *testing.T, opts db.Options, tables ...string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.Connect(context.Background(), opts.ConnString())
	if err != nil {
		t.Fatalf("failed to connect to the test database: %v", err)
	}
	t.Cleanup(pool.Close)

	for _, table := range tables {
		if _, err := pool.Exec(context.Background(), "DELETE FROM "+table); err != nil {
			t.Fatalf("failed to clean table %s: %v", table, err)
		}
	}

	return pool
}
