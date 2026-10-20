package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMarkCloudLaunchCleanedByTokenIsolatesJobsAndTokens is the token-key
// pin. Two jobs that share nothing stay independent, and one job with two
// tokens cleans only the token that was passed. Cleaning by job id would
// mark the other token, and cleaning every row would mark the other job.
func TestMarkCloudLaunchCleanedByTokenIsolatesJobsAndTokens(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })

	const repoURL = "https://example.com/acme/repo.git"
	_, _, jobA := createJobChain(t, db, t.TempDir(), "sha-a")
	_, _, jobB := createJobChain(t, db, t.TempDir(), "sha-b")
	_, err := db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'w' WHERE id IN (?, ?)`, jobA.ID, jobB.ID)
	require.NoError(t, err)
	require.NoError(t, db.BeginCloudLaunch(jobA.ID, "w", "tok-a", repoURL))
	require.NoError(t, db.BeginCloudLaunch(jobB.ID, "w", "tok-b", repoURL))

	require.NoError(t, db.MarkCloudLaunchCleanedByToken(jobA.ID, jobA.RepoID, "tok-a"))

	var cleanedA, cleanedB int
	require.NoError(t, db.QueryRow(`SELECT cleaned FROM review_job_cloud_launches WHERE launch_token = 'tok-a'`).Scan(&cleanedA))
	require.NoError(t, db.QueryRow(`SELECT cleaned FROM review_job_cloud_launches WHERE launch_token = 'tok-b'`).Scan(&cleanedB))
	assert.Equal(t, 1, cleanedA)
	assert.Equal(t, 0, cleanedB)

	_, _, jobC := createJobChain(t, db, t.TempDir(), "sha-c")
	_, err = db.Exec(`UPDATE review_jobs SET status = 'running', worker_id = 'w' WHERE id = ?`, jobC.ID)
	require.NoError(t, err)
	require.NoError(t, db.BeginCloudLaunch(jobC.ID, "w", "tok-c1", repoURL))
	require.NoError(t, db.BeginCloudLaunch(jobC.ID, "w", "tok-c2", repoURL))

	require.NoError(t, db.MarkCloudLaunchCleanedByToken(jobC.ID, jobC.RepoID, "tok-c1"))

	var cleanedC1, cleanedC2 int
	require.NoError(t, db.QueryRow(`SELECT cleaned FROM review_job_cloud_launches WHERE launch_token = 'tok-c1'`).Scan(&cleanedC1))
	require.NoError(t, db.QueryRow(`SELECT cleaned FROM review_job_cloud_launches WHERE launch_token = 'tok-c2'`).Scan(&cleanedC2))
	assert.Equal(t, 1, cleanedC1)
	assert.Equal(t, 0, cleanedC2)
	require.NoError(t, db.QueryRow(`SELECT cleaned FROM review_job_cloud_launches WHERE launch_token = 'tok-b'`).Scan(&cleanedB))
	assert.Equal(t, 0, cleanedB)
}
