package daemon

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/config"
	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/storage"
	"go.kenn.io/roborev/internal/testutil"
)

// enqueueExplicitHostFlag posts --host cursor-cloud with no repo or global
// host and a workflow backup of "test". The flag is the only explicit signal.
func enqueueExplicitHostFlag(t *testing.T) (*Server, *storage.ReviewJob) {
	t.Helper()
	t.Setenv(cursorcloud.EnvAPIKey, "cursor-test-secret")
	t.Setenv("ROBOREV_DEFAULT_HOST", "")
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	cfg.MaxWorkers = 0
	cfg.ReviewBackupAgent = "test"
	server := newServerWithLogs(db, cfg, "", newTestErrorLog(), newTestActivityLog())
	t.Cleanup(func() { _ = server.Close() })

	repo := testutil.InitTestGitRepo(t, filepath.Join(tmpDir, "repo"))
	repo.CommitFile("a.txt", "one\n", "one")
	created := enqueueViaHTTP(t, server, EnqueueRequest{
		RepoPath: repo.Path(),
		GitRef:   "HEAD",
		Host:     agent.CursorCloudHost,
	})
	var explicit int
	require.NoError(t, db.QueryRow(`SELECT explicit_host FROM review_jobs WHERE id = ?`, created.ID).Scan(&explicit))
	require.Equal(t, 1, explicit)
	claimed, err := db.ClaimJob(testWorkerID)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, created.ID, claimed.ID)
	require.True(t, claimed.ExplicitHost)
	require.Empty(t, claimed.BackupAgent)
	return server, claimed
}

func TestExplicitHostFlagPreLaunchDoesNotFailOver(t *testing.T) {
	server, job := enqueueExplicitHostFlag(t)
	assert.Empty(t, server.workerPool.resolveBackupAgent(job))

	server.workerPool.processJob(testWorkerID, job)

	updated, err := server.db.GetJobByID(job.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusFailed, updated.Status)
	assert.Equal(t, agent.CursorCloudHost, updated.Agent)
	assert.Equal(t, 0, updated.RetryCount)
	claimed, claimErr := server.db.ClaimJob(testWorkerID)
	require.NoError(t, claimErr)
	assert.Nil(t, claimed)
}

func TestExplicitHostFlagPostSendDoesNotFailOver(t *testing.T) {
	server, job := enqueueExplicitHostFlag(t)
	assert.Empty(t, server.workerPool.resolveBackupAgent(job))

	server.workerPool.finishAgentError(
		context.Background(), testWorkerID, job, agent.CursorCloudHost,
		fmt.Errorf("create agent: %w", agent.ErrCursorCloudNonRetryable), time.Minute,
	)

	updated, err := server.db.GetJobByID(job.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusFailed, updated.Status)
	assert.Equal(t, agent.CursorCloudHost, updated.Agent)
	assert.Equal(t, 0, updated.RetryCount)
	claimed, claimErr := server.db.ClaimJob(testWorkerID)
	require.NoError(t, claimErr)
	assert.Nil(t, claimed)
}
