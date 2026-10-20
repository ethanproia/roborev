package storage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudLaunchInsertStoresCreatedAt(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	_, _, job := createJobChain(t, db, "/tmp/launch-created", "abc123")
	_, err := db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'w' WHERE id = ?`, job.ID)
	require.NoError(t, err)
	require.NoError(t, db.BeginCloudLaunch(job.ID, "w", "created-tok", "https://example.com/acme/repo.git"))

	var created string
	require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = 'created-tok'`).Scan(&created))
	assert.Regexp(t, sqliteDatetimeText, created)
	parsed, ok := ParseCloudLaunchCreatedAt(created)
	require.True(t, ok)
	assert.Less(t, time.Since(parsed).Abs(), 2*time.Minute)
}

func TestCloudLaunchRebuildCreatedAtDefault(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	_, _, job := createJobChain(t, db, "/tmp/launch-rebuild-created", "abc123")
	_, err := db.Exec(`DROP TABLE review_job_cloud_launches`)
	require.NoError(t, err)
	_, err = db.Exec(`CREATE TABLE review_job_cloud_launches (
		id INTEGER PRIMARY KEY,
		job_id INTEGER NOT NULL REFERENCES review_jobs(id) ON DELETE CASCADE,
		launch_token TEXT NOT NULL,
		agent_id TEXT NOT NULL DEFAULT '',
		cleaned INTEGER NOT NULL DEFAULT 0,
		repo_identity TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL DEFAULT '',
		orphan_note TEXT NOT NULL DEFAULT '')`)
	require.NoError(t, err)
	_, err = db.Exec(`INSERT INTO review_job_cloud_launches
		(job_id, launch_token, created_at) VALUES (?, 'before-rebuild', '2000-01-01 00:00:00')`, job.ID)
	require.NoError(t, err)
	require.NoError(t, db.ensureCloudLaunchTable())

	var kept string
	require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = 'before-rebuild'`).Scan(&kept))
	assert.Equal(t, "2000-01-01 00:00:00", kept)

	_, err = db.Exec(`INSERT INTO review_job_cloud_launches (job_id, launch_token) VALUES (?, 'after-rebuild')`, job.ID)
	require.NoError(t, err)
	var created string
	require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = 'after-rebuild'`).Scan(&created))
	assert.Regexp(t, sqliteDatetimeText, created)
	parsed, ok := ParseCloudLaunchCreatedAt(created)
	require.True(t, ok)
	assert.Less(t, time.Since(parsed).Abs(), 2*time.Minute)
}
