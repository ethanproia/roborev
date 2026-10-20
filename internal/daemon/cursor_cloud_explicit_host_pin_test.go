package daemon

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/config"
	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/cursorcloud/cursorcloudtest"
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

func TestExplicitHostFlagCreate500AfterSendDoesNotFailOver(t *testing.T) {
	server, job := enqueueExplicitHostFlag(t)
	fake := &cursorcloudtest.Fake{MethodErrors: map[string]error{
		cursorcloudtest.MethodCreate: &cursorcloud.APIError{
			Op: "create agent", StatusCode: http.StatusInternalServerError, Message: "unavailable",
		},
		cursorcloudtest.MethodList: &cursorcloud.APIError{
			Op: "list agents", StatusCode: http.StatusTooManyRequests, Message: "slow",
		},
	}}
	prev, err := agent.Get(agent.CursorCloudHost)
	require.NoError(t, err)
	agent.Register(&agent.CursorCloudAgent{
		Client:  fake,
		RepoURL: "https://github.com/acme/api.git",
	})
	t.Cleanup(func() { agent.Register(prev) })

	server.workerPool.processJob(testWorkerID, job)

	creates := 0
	for _, call := range fake.Calls() {
		if call.Method == cursorcloudtest.MethodCreate {
			creates++
		}
	}
	require.Equal(t, 1, creates)
	updated, err := server.db.GetJobByID(job.ID)
	require.NoError(t, err)
	assert.Equal(t, storage.JobStatusFailed, updated.Status)
	assert.Equal(t, agent.CursorCloudHost, updated.Agent)
	assert.Equal(t, 0, updated.RetryCount)
	assert.NotContains(t, updated.Error, "failing over")
	claimed, claimErr := server.db.ClaimJob(testWorkerID)
	require.NoError(t, claimErr)
	assert.Nil(t, claimed)
}

// enqueueCursorCloudHostSource enqueues one ordinary review whose cursor-cloud
// host comes from exactly one source, with a workflow backup of "test".
func enqueueCursorCloudHostSource(t *testing.T, source string) (*Server, *storage.ReviewJob) {
	t.Helper()
	t.Setenv(cursorcloud.EnvAPIKey, "cursor-test-secret")
	t.Setenv(agent.EnvDefaultHost, "")
	db, tmpDir := testutil.OpenTestDBWithDir(t)
	cfg := config.DefaultConfig()
	cfg.MaxWorkers = 0
	cfg.ReviewBackupAgent = "test"
	req := EnqueueRequest{GitRef: "HEAD"}
	switch source {
	case "config host":
		cfg.Host = agent.CursorCloudHost
	case "--host":
		req.Host = agent.CursorCloudHost
	case "ROBOREV_DEFAULT_HOST":
		t.Setenv(agent.EnvDefaultHost, agent.CursorCloudHost)
	}
	server := newServerWithLogs(db, cfg, "", newTestErrorLog(), newTestActivityLog())
	t.Cleanup(func() { _ = server.Close() })

	repo := testutil.InitTestGitRepo(t, filepath.Join(tmpDir, "repo"))
	repo.CommitFile("a.txt", "one\n", "one")
	req.RepoPath = repo.Path()
	created := enqueueViaHTTP(t, server, req)
	claimed, err := db.ClaimJob(testWorkerID)
	require.NoError(t, err)
	require.NotNil(t, claimed)
	require.Equal(t, created.ID, claimed.ID)
	require.Equal(t, agent.CursorCloudHost, claimed.Agent)
	require.Empty(t, claimed.BackupAgent)
	return server, claimed
}

// TestCursorCloudHostSourcesCreate500DoNotFailOver requires config host,
// --host, and ROBOREV_DEFAULT_HOST to store the same bit and to refuse a
// workflow backup after a create 500. The config-host outcome is the bar:
// the job fails on cursor-cloud and is not retried. A cursor-cloud agent
// with no host source may still fail over; cursor_cloud_retry_test.go pins that.
func TestCursorCloudHostSourcesCreate500DoNotFailOver(t *testing.T) {
	for _, source := range []string{"config host", "--host", "ROBOREV_DEFAULT_HOST"} {
		t.Run(source, func(t *testing.T) {
			assert := assert.New(t)
			server, job := enqueueCursorCloudHostSource(t, source)
			var explicit int
			require.NoError(t, server.db.QueryRow(
				`SELECT explicit_host FROM review_jobs WHERE id = ?`, job.ID,
			).Scan(&explicit))
			assert.Equal(1, explicit)
			assert.True(job.ExplicitHost)
			assert.Empty(server.workerPool.resolveBackupAgent(job))

			fake := &cursorcloudtest.Fake{MethodErrors: map[string]error{
				cursorcloudtest.MethodCreate: &cursorcloud.APIError{
					Op: "create agent", StatusCode: http.StatusInternalServerError, Message: "unavailable",
				},
				cursorcloudtest.MethodList: &cursorcloud.APIError{
					Op: "list agents", StatusCode: http.StatusTooManyRequests, Message: "slow",
				},
			}}
			prev, err := agent.Get(agent.CursorCloudHost)
			require.NoError(t, err)
			agent.Register(&agent.CursorCloudAgent{
				Client:  fake,
				RepoURL: "https://github.com/acme/api.git",
			})
			t.Cleanup(func() { agent.Register(prev) })

			server.workerPool.processJob(testWorkerID, job)

			creates := 0
			for _, call := range fake.Calls() {
				if call.Method == cursorcloudtest.MethodCreate {
					creates++
				}
			}
			assert.Equal(1, creates)
			updated, err := server.db.GetJobByID(job.ID)
			require.NoError(t, err)
			assert.Equal(storage.JobStatusFailed, updated.Status)
			assert.Equal(agent.CursorCloudHost, updated.Agent)
			assert.Equal(0, updated.RetryCount)
			assert.NotContains(updated.Error, "failing over")
			claimed, claimErr := server.db.ClaimJob(testWorkerID)
			require.NoError(t, claimErr)
			assert.Nil(claimed)
		})
	}
}
