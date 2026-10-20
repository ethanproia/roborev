package storage

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOpenQuarantinesDuplicateLaunchTokens is the upgrade pin. Tokens were
// not unique, so a database can already hold duplicates. Open must succeed.
// Removing the quarantine migration makes the unique index fail and Open
// returns an error.
//
// The seed is an uncleaned row and a cleaned row with one token, plus a
// pair of uncleaned rows with one token on two repos. The cleaned row is
// the earliest of its token. A migration that drops later rows would lose
// the uncleaned launch. ListUncleanedCloudLaunches is what the orphan
// sweep reads.
func TestOpenQuarantinesDuplicateLaunchTokens(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	_, err := db.Exec(`DROP INDEX IF EXISTS idx_review_job_cloud_launches_token`)
	require.NoError(t, err)

	repoA, _, jobA := createJobChain(t, db, filepath.Join(t.TempDir(), "repo-a"), "aaa111")
	commitA2 := createCommit(t, db, repoA.ID, "aaa222")
	jobA2 := enqueueJob(t, db, repoA.ID, commitA2.ID, "aaa222")
	commitA3 := createCommit(t, db, repoA.ID, "aaa333")
	jobA3 := enqueueJob(t, db, repoA.ID, commitA3.ID, "aaa333")
	repoB, _, jobB := createJobChain(t, db, filepath.Join(t.TempDir(), "repo-b"), "bbb111")

	const tokRepos = "tok-repos"
	const tokSame = "tok-same"
	insert := func(jobID int64, token, agent string, cleaned int, repoURL string) {
		t.Helper()
		_, err := db.Exec(`INSERT INTO review_job_cloud_launches
			(job_id, launch_token, agent_id, cleaned, repo_identity, orphan_note)
			VALUES (?, ?, ?, ?, ?, 'seed-note')`, jobID, token, agent, cleaned, repoURL)
		require.NoError(t, err)
	}
	insert(jobA.ID, tokRepos, "repos-keep", 0, "https://example.com/a.git")
	insert(jobB.ID, tokRepos, "repos-dup", 0, "https://example.com/b.git")
	insert(jobA2.ID, tokSame, "same-cleaned", 1, "https://example.com/a.git")
	insert(jobA3.ID, tokSame, "same-live", 0, "https://example.com/a.git")

	var before int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM review_job_cloud_launches`).Scan(&before))
	require.Equal(t, 4, before)

	var path string
	require.NoError(t, db.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path))
	require.NoError(t, db.Close())

	opened, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = opened.Close() })

	var indexSQL string
	require.NoError(t, opened.QueryRow(
		`SELECT sql FROM sqlite_master WHERE type = 'index' AND name = 'idx_review_job_cloud_launches_token'`,
	).Scan(&indexSQL))
	require.Contains(t, strings.ToUpper(indexSQL), "UNIQUE")

	type stored struct {
		jobID               int64
		token, repoURL      string
		cleaned, quarantine int
	}
	load := func(agent string) stored {
		t.Helper()
		var row stored
		require.NoError(t, opened.QueryRow(`SELECT job_id, launch_token, cleaned, token_quarantine, repo_identity
			FROM review_job_cloud_launches WHERE agent_id = ?`, agent).Scan(
			&row.jobID, &row.token, &row.cleaned, &row.quarantine, &row.repoURL))
		return row
	}
	keep := load("repos-keep")
	dup := load("repos-dup")
	cleaned := load("same-cleaned")
	live := load("same-live")

	assert := assert.New(t)
	var after int
	require.NoError(t, opened.QueryRow(`SELECT COUNT(*) FROM review_job_cloud_launches`).Scan(&after))
	assert.Equal(4, after)
	assert.Equal(0, keep.quarantine)
	assert.Equal(0, keep.cleaned)
	assert.Equal(tokRepos, keep.token)
	assert.Equal(jobA.ID, keep.jobID)
	assert.Equal("https://example.com/a.git", keep.repoURL)
	assert.Equal(1, dup.quarantine)
	assert.Equal(0, dup.cleaned)
	assert.Equal(tokRepos, dup.token)
	assert.Equal(jobB.ID, dup.jobID)
	assert.Equal("https://example.com/b.git", dup.repoURL)
	assert.Equal(0, cleaned.quarantine)
	assert.Equal(1, cleaned.cleaned)
	assert.Equal(tokSame, cleaned.token)
	assert.Equal(jobA2.ID, cleaned.jobID)
	assert.Equal(1, live.quarantine)
	assert.Equal(0, live.cleaned)
	assert.Equal(tokSame, live.token)
	assert.Equal(jobA3.ID, live.jobID)

	rows, err := opened.ListUncleanedCloudLaunches()
	require.NoError(t, err)
	byAgent := make(map[string]CloudLaunch, len(rows))
	for _, row := range rows {
		byAgent[row.AgentID] = row
	}
	_, cleanedListed := byAgent["same-cleaned"]
	assert.False(cleanedListed)
	for _, want := range []struct {
		agent  string
		token  string
		jobID  int64
		repoID int64
	}{
		{"repos-keep", tokRepos, jobA.ID, repoA.ID},
		{"repos-dup", tokRepos, jobB.ID, repoB.ID},
		{"same-live", tokSame, jobA3.ID, repoA.ID},
	} {
		row, ok := byAgent[want.agent]
		require.True(t, ok)
		assert.Equal(want.token, row.Token)
		assert.Equal(want.jobID, row.JobID)
		assert.Equal(want.repoID, row.RepoID)
		assert.False(row.TokenNull)
	}

	require.NoError(t, opened.Close())
	again, err := Open(path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = again.Close() })
	var still int
	require.NoError(t, again.QueryRow(`SELECT COUNT(*) FROM review_job_cloud_launches WHERE cleaned = 0`).Scan(&still))
	assert.Equal(3, still)
	listed, err := again.ListUncleanedCloudLaunches()
	require.NoError(t, err)
	saw := map[string]bool{}
	for _, row := range listed {
		saw[row.AgentID] = true
		assert.NotEqual("same-cleaned", row.AgentID)
	}
	assert.True(saw["repos-keep"])
	assert.True(saw["repos-dup"])
	assert.True(saw["same-live"])
}

// TestQuarantinedLaunchNotCleanedByToken is the token-clean pin. A
// quarantined row shares its token with a live row. Cleaning by token
// marks the live row and leaves the quarantined row uncleaned and listed.
// Dropping the token_quarantine predicate cleans both and fails this test.
func TestQuarantinedLaunchNotCleanedByToken(t *testing.T) {
	t.Parallel()
	db := openTestDB(t)
	t.Cleanup(func() { db.Close() })
	repo, _, job := createJobChain(t, db, filepath.Join(t.TempDir(), "quarantine"), "qqq111")
	_, err := db.Exec(`INSERT INTO review_job_cloud_launches
		(job_id, launch_token, agent_id, cleaned, token_quarantine, orphan_note) VALUES
		(?, 'q-tok', 'live', 0, 0, 'live-note'),
		(?, 'q-tok', 'quarantine', 0, 1, 'q-note')`, job.ID, job.ID)
	require.NoError(t, err)

	require.NoError(t, db.MarkCloudLaunchCleanedByToken(job.ID, repo.ID, "q-tok"))

	cleaned := func(agent string) int {
		t.Helper()
		var n int
		require.NoError(t, db.QueryRow(
			`SELECT cleaned FROM review_job_cloud_launches WHERE agent_id = ?`, agent,
		).Scan(&n))
		return n
	}
	assert.Equal(t, 1, cleaned("live"))
	assert.Equal(t, 0, cleaned("quarantine"))

	rows, err := db.ListUncleanedCloudLaunches()
	require.NoError(t, err)
	var found CloudLaunch
	var n int
	for _, row := range rows {
		if row.AgentID != "quarantine" {
			continue
		}
		n++
		found = row
	}
	require.Equal(t, 1, n)
	assert.Equal(t, "q-tok", found.Token)
	assert.True(t, found.TokenQuarantine)
	assert.Equal(t, job.ID, found.JobID)
	assert.Equal(t, repo.ID, found.RepoID)
}
