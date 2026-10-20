package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDropCloudLaunchJobFKSQLTargetsOwnedJobConstraint(t *testing.T) {
	t.Parallel()
	sql := dropCloudLaunchJobFKSQL
	assert.Contains(t, sql, "n.nspname = '"+pgSchemaName+"'")
	assert.Contains(t, sql, "fn.nspname = '"+pgSchemaName+"'")
	assert.Contains(t, sql, "a.attname = 'job_id'")
	assert.Contains(t, sql, "f.relname = 'review_jobs'")
	assert.Contains(t, sql, "fa.attname = 'id'")
	assert.Contains(t, sql, "cardinality(con.conkey) = 1")
	assert.Contains(t, sql, "ALTER TABLE %I.%I DROP CONSTRAINT %I', '"+pgSchemaName+"'")
	assert.NotContains(t, sql, "r.nspname")
}

func TestPostgresDropCloudLaunchJobFKLeavesOtherConstraints(t *testing.T) { //nolint:paralleltest // shares the roborev schema in the PostgreSQL database at TEST_POSTGRES_URL
	pool := openTestPgPool(t)
	ctx := t.Context()
	pool.ensureLocalCloudLaunchTable(ctx)

	other := "fkpin_" + strings.ReplaceAll(uuid.New().String(), "-", "")[:12]
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.pool.Exec(bg, `ALTER TABLE roborev.review_job_cloud_launches DROP CONSTRAINT IF EXISTS fkpin_repo_fkey`)
		_, _ = pool.pool.Exec(bg, `ALTER TABLE roborev.review_job_cloud_launches DROP CONSTRAINT IF EXISTS fkpin_job_fkey`)
		_, _ = pool.pool.Exec(bg, `ALTER TABLE roborev.review_job_cloud_launches DROP COLUMN IF EXISTS fkpin_repo`)
		_, _ = pool.pool.Exec(bg, fmt.Sprintf(`DROP SCHEMA IF EXISTS %s CASCADE`, other))
	})

	_, err := pool.pool.Exec(ctx, fmt.Sprintf(`CREATE SCHEMA %s`, other))
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s.review_jobs (id BIGSERIAL PRIMARY KEY)`, other))
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, fmt.Sprintf(`CREATE TABLE %s.review_job_cloud_launches (
		id BIGSERIAL PRIMARY KEY,
		job_id BIGINT REFERENCES %s.review_jobs(id) ON DELETE CASCADE)`, other, other))
	require.NoError(t, err)

	_, err = pool.pool.Exec(ctx, `ALTER TABLE roborev.review_job_cloud_launches ADD COLUMN IF NOT EXISTS fkpin_repo BIGINT`)
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, `ALTER TABLE roborev.review_job_cloud_launches DROP CONSTRAINT IF EXISTS fkpin_job_fkey`)
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, `ALTER TABLE roborev.review_job_cloud_launches
		ADD CONSTRAINT fkpin_job_fkey FOREIGN KEY (job_id) REFERENCES roborev.review_jobs(id) ON DELETE CASCADE NOT VALID`)
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, `ALTER TABLE roborev.review_job_cloud_launches DROP CONSTRAINT IF EXISTS fkpin_repo_fkey`)
	require.NoError(t, err)
	_, err = pool.pool.Exec(ctx, `ALTER TABLE roborev.review_job_cloud_launches
		ADD CONSTRAINT fkpin_repo_fkey FOREIGN KEY (fkpin_repo) REFERENCES roborev.repos(id) ON DELETE CASCADE NOT VALID`)
	require.NoError(t, err)

	pool.ensureLocalCloudLaunchTable(ctx)

	var jobFK, repoFK, otherFK int
	err = pool.pool.QueryRow(ctx, `SELECT COUNT(*)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = con.conkey[1]
		WHERE n.nspname = 'roborev' AND c.relname = 'review_job_cloud_launches'
		  AND con.contype = 'f' AND a.attname = 'job_id'`).Scan(&jobFK)
	require.NoError(t, err)
	assert.Equal(t, 0, jobFK)

	err = pool.pool.QueryRow(ctx, `SELECT COUNT(*)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'roborev' AND c.relname = 'review_job_cloud_launches'
		  AND con.contype = 'f' AND con.conname = 'fkpin_repo_fkey'`).Scan(&repoFK)
	require.NoError(t, err)
	assert.Equal(t, 1, repoFK)

	err = pool.pool.QueryRow(ctx, `SELECT COUNT(*)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = $1 AND c.relname = 'review_job_cloud_launches' AND con.contype = 'f'`, other).Scan(&otherFK)
	require.NoError(t, err)
	assert.Equal(t, 1, otherFK)
}
