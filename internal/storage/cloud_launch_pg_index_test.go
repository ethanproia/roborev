package storage

import (
	"bytes"
	"context"
	"log/slog"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPostgresMissingLaunchTokenIndexFailSoft is the pin for a postgres
// startup that cannot build the launch-token index. EnsureSchema still
// returns nil. The missing index is logged at error level and surfaced on
// the pool and the sync health check.
func TestPostgresMissingLaunchTokenIndexFailSoft(t *testing.T) { //nolint:paralleltest // shares the roborev schema in the PostgreSQL database at TEST_POSTGRES_URL
	pool := openTestPgPool(t)
	ctx := t.Context()
	token := "c10n2-missing-index-" + uuid.New().String()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.pool.Exec(bg, `DELETE FROM review_job_cloud_launches WHERE launch_token = $1`, token)
		_, _ = pool.pool.Exec(bg, `CREATE UNIQUE INDEX IF NOT EXISTS idx_review_job_cloud_launches_token
			ON review_job_cloud_launches (launch_token) WHERE launch_token <> ''`)
	})

	_, err := pool.pool.Exec(ctx, `DROP INDEX IF EXISTS idx_review_job_cloud_launches_token`)
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, `INSERT INTO review_job_cloud_launches (job_id, launch_token) VALUES (1, $1), (2, $1)`, token)
	require.NoError(t, err)

	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	require.NoError(t, pool.EnsureSchema(ctx))

	var n int
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM pg_indexes
		WHERE schemaname = current_schema() AND indexname = 'idx_review_job_cloud_launches_token'`).Scan(&n))
	problem := pool.CloudLaunchIndexProblem()
	worker := &SyncWorker{running: true, pgPool: pool}
	healthy, msg := worker.HealthCheck()

	assert := assert.New(t)
	assert.Equal(0, n)
	assert.Contains(problem, "missing")
	assert.Contains(logs.String(), "level=ERROR")
	assert.Contains(logs.String(), "missing")
	assert.False(healthy)
	assert.Equal(problem, msg)
	require.NoError(t, pool.Ping(ctx))
}
