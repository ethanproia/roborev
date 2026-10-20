package daemon

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/agent"
	"go.kenn.io/roborev/internal/cursorcloud/cursorcloudtest"
	"go.kenn.io/roborev/internal/storage"
	"go.kenn.io/roborev/internal/testutil"
)

// TestQuarantinedLaunchStaysInRestartOrphanReport is the reporting pin.
// A quarantined uncleaned row stays in ListUncleanedCloudLaunches and the
// restart orphan report. The by-token sweep does not stop or delete its
// agent and does not mark the row cleaned. Removing the quarantine branch
// in recoverOneCloudLaunch deletes the agent and fails this test.
func TestQuarantinedLaunchStaysInRestartOrphanReport(t *testing.T) {
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	const token = "quarantine-report-tok"
	res, err := tc.DB.Exec(`INSERT INTO review_job_cloud_launches
		(job_id, launch_token, repo_identity, orphan_note, cleaned, token_quarantine)
		VALUES (?, ?, ?, ?, 0, 1)`,
		job.ID, token, cursorCloudRestartRepo, storage.CloudLaunchPendingNote)
	require.NoError(t, err)
	launchID, err := res.LastInsertId()
	require.NoError(t, err)

	machineID, err := tc.DB.GetMachineID()
	require.NoError(t, err)
	created := cursorCloudSeedAgent(t, fake, sha, agent.CursorCloudLaunchMarker(machineID.String(), token)) //nolint:forbidigo // launch marker is prompt text
	_, err = tc.DB.Exec(`UPDATE review_job_cloud_launches SET agent_id = ? WHERE id = ?`, created.ID, launchID)
	require.NoError(t, err)

	rows, err := tc.DB.ListUncleanedCloudLaunches()
	require.NoError(t, err)
	listed := quarantinedLaunchByID(rows, launchID)
	require.Equal(t, token, listed.Token)
	require.True(t, listed.TokenQuarantine)
	require.Equal(t, job.ID, listed.JobID)

	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	require.NoError(t, recoverCloudLaunches(context.Background(), tc.DB, fake))

	want := fmt.Sprintf("cursor-cloud orphan risk: job %d launch %d was not listed", job.ID, launchID)
	assert.Contains(t, logs.String(), want)
	var jobErr string
	var cleaned int
	require.NoError(t, tc.DB.QueryRow(`SELECT COALESCE(error, '') FROM review_jobs WHERE id = ?`, job.ID).Scan(&jobErr))
	require.NoError(t, tc.DB.QueryRow(`SELECT cleaned FROM review_job_cloud_launches WHERE id = ?`, launchID).Scan(&cleaned))
	assert.Contains(t, jobErr, want)
	assert.Equal(t, 0, cleaned)
	assert.Empty(t, cursorCloudCallIDs(fake, cursorcloudtest.MethodStop))
	assert.Empty(t, cursorCloudCallIDs(fake, cursorcloudtest.MethodDelete))

	again, err := tc.DB.ListUncleanedCloudLaunches()
	require.NoError(t, err)
	still := quarantinedLaunchByID(again, launchID)
	assert.Equal(t, token, still.Token)
	assert.True(t, still.TokenQuarantine)
}

func quarantinedLaunchByID(rows []storage.CloudLaunch, id int64) storage.CloudLaunch {
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	return storage.CloudLaunch{}
}
