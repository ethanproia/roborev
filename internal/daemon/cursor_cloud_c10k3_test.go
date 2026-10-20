package daemon

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/cursorcloud"
	"go.kenn.io/roborev/internal/cursorcloud/cursorcloudtest"
	"go.kenn.io/roborev/internal/storage"
	"go.kenn.io/roborev/internal/testutil"
)

// TestRecoverStaleJobsSweepsStaleCloudLaunch is the h/m2a pin. A daemon
// restart with a stale, uncleaned launch row has to reach
// recoverCloudLaunches, which lists that row. The package recover returns
// the not-listed error from that same call. Removing either call fails
// this test: the daemon sweep records no list, and the package function
// returns nil.
func TestRecoverStaleJobsSweepsStaleCloudLaunch(t *testing.T) {
	t.Run("daemon", func(t *testing.T) {
		tc, fake, job := staleUncleanedCloudLaunch(t)
		srv := &Server{db: tc.DB, cloudLaunchClient: fake}

		require.NoError(t, srv.recoverStaleJobs(context.Background()))

		assertStaleLaunchListed(t, tc, fake, job.ID)
	})
	t.Run("package", func(t *testing.T) {
		tc, fake, job := staleUncleanedCloudLaunch(t)

		err := recoverStaleJobs(context.Background(), tc.DB, fake)

		require.ErrorContains(t, err, "not listed yet")
		assertStaleLaunchListed(t, tc, fake, job.ID)
	})
}

func staleUncleanedCloudLaunch(t *testing.T) (*workerTestContext, *cursorcloudtest.Fake, *storage.ReviewJob) {
	t.Helper()
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	require.NoError(t, tc.DB.BeginCloudLaunch(job.ID, testWorkerID, "tok-stale-restart", cursorCloudRestartRepo))
	return tc, fake, job
}

func assertStaleLaunchListed(t *testing.T, tc *workerTestContext, fake *cursorcloudtest.Fake, jobID int64) {
	t.Helper()
	assert.NotEmpty(t, cursorCloudCallIDs(fake, cursorcloudtest.MethodList))
	// A young miss is not a confirmed delete. The row stays uncleaned.
	n, err := tc.DB.CountUncleanedCloudLaunches(jobID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// TestBoundCloudAgentIDIsDeletedOnRestart is the i3/m7 pin. After a launch,
// the row carries the id BindAgent stored, and the restart sweep's DELETE
// targets that id. The marker list is failed so the id cannot be discovered
// any other way. A no-op BindAgent leaves the id empty and the DELETE does
// not run.
func TestBoundCloudAgentIDIsDeletedOnRestart(t *testing.T) {
	t.Setenv(cursorcloud.EnvAPIKey, "test-key")
	tc, fake := cursorCloudRestartFixture(t)
	fake.CreateStatus = cursorcloud.StatusFinished
	fake.Final = cursorCloudPassJSON
	fake.MethodErrors = map[string]error{
		cursorcloudtest.MethodDelete: &cursorcloud.APIError{
			Op: "delete agent", StatusCode: http.StatusBadRequest, Message: "refused",
		},
	}
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)

	tc.Pool.processJob(testWorkerID, job)

	live, err := fake.ListAgents(t.Context())
	require.NoError(t, err)
	require.Len(t, live, 1)
	boundID := cursorCloudLaunchAgentID(t, tc.DB, job.ID)
	assert.Equal(t, live[0].ID, boundID)

	before := len(cursorCloudCallIDs(fake, cursorcloudtest.MethodDelete))
	fake.MethodErrors = map[string]error{
		cursorcloudtest.MethodList: &cursorcloud.APIError{
			Op: "list agents", StatusCode: http.StatusInternalServerError, Message: "unavailable",
		},
	}
	srv := &Server{db: tc.DB, cloudLaunchClient: fake}
	require.NoError(t, srv.recoverStaleJobs(context.Background()))

	deletes := cursorCloudCallIDs(fake, cursorcloudtest.MethodDelete)
	require.Greater(t, len(deletes), before)
	assert.Equal(t, boundID, deletes[before])
}
