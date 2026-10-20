//go:build postgres

package storage

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// e387fe29 created review_job_cloud_launches with no created_at and no
// repo_identity. EnsureSchema has to converge that table onto a fresh
// install without dropping rows.
const e387CloudLaunchDDL = `CREATE TABLE roborev.review_job_cloud_launches (
	id BIGSERIAL PRIMARY KEY,
	job_id INTEGER NOT NULL REFERENCES roborev.review_jobs(id) ON DELETE CASCADE,
	launch_token TEXT NOT NULL,
	agent_id TEXT NOT NULL DEFAULT '',
	cleaned BOOLEAN NOT NULL DEFAULT FALSE)`

func TestIntegration_EnsureSchema_UpgradesE387CloudLaunches(t *testing.T) { //nolint:paralleltest // shares the roborev schema in the PostgreSQL database at TEST_POSTGRES_URL
	pool := openTestPgPoolRawAtVersion(t, 23)
	ctx := t.Context()
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = pool.pool.Exec(bg, `DROP TABLE IF EXISTS roborev.review_job_cloud_launches`)
		pool.ensureLocalCloudLaunchTable(bg)
	})

	_, err := pool.pool.Exec(ctx, e387CloudLaunchDDL)
	require.NoError(t, err)

	const repoIdentity = "https://example.com/acme/e387.git"
	var repoID int64
	require.NoError(t, pool.pool.QueryRow(ctx,
		`INSERT INTO roborev.repos (identity) VALUES ($1) RETURNING id`, repoIdentity).Scan(&repoID))
	machineID := uuid.New()
	started := time.Date(2020, 1, 2, 4, 5, 6, 0, time.UTC)
	enqueuedOnly := time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)
	cleanedStart := time.Date(2020, 5, 6, 7, 8, 9, 0, time.UTC)

	jobWithStart := insertE387Job(t, ctx, pool, repoID, machineID, started.Add(-time.Hour), &started)
	jobEnqueued := insertE387Job(t, ctx, pool, repoID, machineID, enqueuedOnly, nil)
	jobCleaned := insertE387Job(t, ctx, pool, repoID, machineID, cleanedStart.Add(-time.Hour), &cleanedStart)

	type seeded struct {
		id              int64
		jobID           int64
		token, agentID  string
		cleaned         bool
		wantCreatedUnix int64
	}
	rows := []seeded{
		{jobID: jobWithStart, token: "tok-started", agentID: "bc_started", wantCreatedUnix: started.Unix()},
		{jobID: jobEnqueued, token: "", agentID: "bc_blank", wantCreatedUnix: enqueuedOnly.Unix()},
		{jobID: jobCleaned, token: "tok-cleaned", agentID: "bc_cleaned", cleaned: true, wantCreatedUnix: cleanedStart.Unix()},
	}
	for i := range rows {
		require.NoError(t, pool.pool.QueryRow(ctx, `
			INSERT INTO roborev.review_job_cloud_launches (job_id, launch_token, agent_id, cleaned)
			VALUES ($1, $2, $3, $4) RETURNING id`,
			rows[i].jobID, rows[i].token, rows[i].agentID, rows[i].cleaned).Scan(&rows[i].id))
	}
	var before int
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM roborev.review_job_cloud_launches`).Scan(&before))
	require.Equal(t, len(rows), before)

	require.NoError(t, pool.EnsureSchema(ctx))

	var after int
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM roborev.review_job_cloud_launches`).Scan(&after))
	require.Equal(t, before, after)

	assert := assert.New(t)
	for _, row := range rows {
		var token, agentID, identity, note string
		var cleaned bool
		var created time.Time
		var id, jobID int64
		require.NoError(t, pool.pool.QueryRow(ctx, `
			SELECT id, job_id, launch_token, agent_id, cleaned, repo_identity, created_at, orphan_note
			FROM roborev.review_job_cloud_launches WHERE id = $1`, row.id).Scan(
			&id, &jobID, &token, &agentID, &cleaned, &identity, &created, &note))
		assert.Equal(row.id, id)
		assert.Equal(row.jobID, jobID)
		assert.Equal(row.token, token)
		assert.Equal(row.agentID, agentID)
		assert.Equal(row.cleaned, cleaned)
		// sqlite ADD COLUMN repo_identity DEFAULT '' leaves the old row empty.
		assert.Empty(identity)
		assert.Equal(row.wantCreatedUnix, created.UTC().Unix())
		if row.cleaned {
			assert.Empty(note)
		} else {
			assert.Equal(CloudLaunchPendingNote, note)
		}
	}

	// Restart lists uncleaned launches. The orphan list is the same rows
	// once the job is gone. Both reads are the storage queries, with
	// boolean cleaned instead of the sqlite 0/1.
	type listed struct {
		id, jobID      int64
		token, agentID string
		note           string
	}
	var uncleaned []listed
	list, err := pool.pool.Query(ctx, `
		SELECT l.id, l.job_id, l.launch_token, COALESCE(l.agent_id, ''),
			COALESCE(l.repo_identity, ''), COALESCE(l.orphan_note, '')
		FROM roborev.review_job_cloud_launches l
		LEFT JOIN roborev.review_jobs j ON j.id = l.job_id
		LEFT JOIN roborev.repos r ON r.id = j.repo_id
		WHERE NOT l.cleaned
		ORDER BY l.id`)
	require.NoError(t, err)
	for list.Next() {
		var row listed
		var identity string
		require.NoError(t, list.Scan(&row.id, &row.jobID, &row.token, &row.agentID, &identity, &row.note))
		assert.Empty(identity)
		assert.Equal(CloudLaunchPendingNote, row.note)
		uncleaned = append(uncleaned, row)
	}
	require.NoError(t, list.Err())
	list.Close()
	require.Len(t, uncleaned, 2)
	assert.Equal("tok-started", uncleaned[0].token)
	assert.Equal("bc_started", uncleaned[0].agentID)
	assert.Empty(uncleaned[1].token)
	assert.Equal("bc_blank", uncleaned[1].agentID)

	_, err = pool.pool.Exec(ctx, `DELETE FROM roborev.review_jobs WHERE id = $1`, jobEnqueued)
	require.NoError(t, err)
	var still int
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM roborev.review_job_cloud_launches`).Scan(&still))
	assert.Equal(before, still)

	var orphanJob int64
	var orphanAgent, orphanNote string
	require.NoError(t, pool.pool.QueryRow(ctx, `
		SELECT l.job_id, COALESCE(l.agent_id, ''), l.orphan_note
		FROM roborev.review_job_cloud_launches l
		LEFT JOIN roborev.review_jobs j ON j.id = l.job_id
		WHERE NOT l.cleaned AND l.orphan_note <> '' AND j.id IS NULL
		ORDER BY l.id`).Scan(&orphanJob, &orphanAgent, &orphanNote))
	assert.Equal(jobEnqueued, orphanJob)
	assert.Equal("bc_blank", orphanAgent)
	assert.Equal(CloudLaunchPendingNote, orphanNote)

	upgraded := pgCloudLaunchFingerprint(t, ctx, pool)
	_, err = pool.pool.Exec(ctx, `DROP TABLE roborev.review_job_cloud_launches`)
	require.NoError(t, err)
	pool.ensureLocalCloudLaunchTable(ctx)
	assert.Equal(pgCloudLaunchFingerprint(t, ctx, pool), upgraded)
}

func TestIntegration_EnsureSchema_V24(t *testing.T) { //nolint:paralleltest // shares the roborev schema in the PostgreSQL database at TEST_POSTGRES_URL
	pool := openTestPgPoolRawAtVersion(t, 23)
	ctx := t.Context()
	t.Cleanup(func() {
		_, _ = pool.pool.Exec(context.Background(), `DELETE FROM roborev.schema_version WHERE version = 24`)
	})

	require.NoError(t, pool.EnsureSchema(ctx))

	var version int
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT MAX(version) FROM roborev.schema_version`).Scan(&version))
	assert.Equal(t, 23, version)
	assert.Equal(t, 23, pgSchemaVersion)
	var written int
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT COUNT(*) FROM roborev.schema_version WHERE version = 24`).Scan(&written))
	assert.Equal(t, 0, written)

	cols := pgColumnNames(t, ctx, pool, "review_job_cloud_launches")
	assert.Contains(t, cols, "created_at")
	assert.Contains(t, cols, "repo_identity")
	assert.Contains(t, cols, "orphan_note")

	_, err := pool.pool.Exec(ctx, `INSERT INTO roborev.schema_version (version) VALUES (24)`)
	require.NoError(t, err)
	err = pool.EnsureSchema(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "schema version 24 is newer")
}

func insertE387Job(t *testing.T, ctx context.Context, pool *PgPool, repoID int64, machineID uuid.UUID, enqueued time.Time, started *time.Time) int64 {
	t.Helper()
	var id int64
	require.NoError(t, pool.pool.QueryRow(ctx, `
		INSERT INTO roborev.review_jobs
			(uuid, repo_id, git_ref, agent, status, enqueued_at, started_at, source_machine_id)
		VALUES ($1, $2, 'e387', 'cursor-cloud', 'running', $3, $4, $5)
		RETURNING id`, uuid.New(), repoID, enqueued, started, machineID).Scan(&id))
	return id
}

func pgCloudLaunchFingerprint(t *testing.T, ctx context.Context, pool *PgPool) string {
	t.Helper()
	var b strings.Builder
	writeQuery := func(title, query string) {
		rows, err := pool.pool.Query(ctx, query)
		require.NoError(t, err)
		defer rows.Close()
		fmt.Fprintf(&b, "%s\n", title)
		for rows.Next() {
			vals, err := rows.Values()
			require.NoError(t, err)
			parts := make([]string, len(vals))
			for i, val := range vals {
				parts[i] = fmt.Sprint(val)
			}
			fmt.Fprintf(&b, "%s\n", strings.Join(parts, "\t"))
		}
		require.NoError(t, rows.Err())
	}
	writeQuery("columns", `
		SELECT column_name, data_type, is_nullable, COALESCE(column_default, '')
		FROM information_schema.columns
		WHERE table_schema = 'roborev' AND table_name = 'review_job_cloud_launches'
		ORDER BY column_name`)
	writeQuery("indexes", `
		SELECT indexdef FROM pg_indexes
		WHERE schemaname = 'roborev' AND tablename = 'review_job_cloud_launches'
		ORDER BY indexdef`)
	writeQuery("constraints", `
		SELECT con.contype::text, pg_get_constraintdef(con.oid)
		FROM pg_constraint con
		JOIN pg_class c ON c.oid = con.conrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE n.nspname = 'roborev' AND c.relname = 'review_job_cloud_launches'
		ORDER BY 1, 2`)
	return b.String()
}
