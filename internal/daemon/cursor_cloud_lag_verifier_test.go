package daemon

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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

// hideOnFirstList is the delayed-duplicate list. The first ListAgents omits
// hideID. The next list returns it. A delete of an id the fake already
// dropped is a 404, which is the gone result the cleanup path accepts.
type hideOnFirstList struct {
	cursorcloud.Client
	hideID string
	lists  int
}

func (h *hideOnFirstList) ListAgents(ctx context.Context) ([]cursorcloud.Agent, error) {
	h.lists++
	agents, err := h.Client.ListAgents(ctx)
	if err != nil || h.lists > 1 {
		return agents, err
	}
	visible := make([]cursorcloud.Agent, 0, len(agents))
	for _, ag := range agents {
		if ag.ID != h.hideID {
			visible = append(visible, ag)
		}
	}
	return visible, nil
}

func (h *hideOnFirstList) DeleteAgent(ctx context.Context, id string) error {
	err := h.Client.DeleteAgent(ctx, id)
	if err != nil && strings.Contains(err.Error(), "not found") {
		return &cursorcloud.APIError{Op: "delete agent", StatusCode: http.StatusNotFound, Message: "not found"}
	}
	return err
}

// TestCursorCloudDelayedDuplicateStaysUncleanedUntilLaterEmptySweep is the
// listing-lag repro. The first post-delete list hides the second same-token
// VM. The restart that deletes the duplicate still does not clean. The next
// restart's empty list is the first empty after that find, so it does not
// clean either: a delayed duplicate can still be hidden. The empty list
// after that one cleans only after the delay that actually won.
// Repo 0, a negative repo value, and an unparseable repo value clamp to
// 1 hour, so a launch younger than that stays uncleaned. A valid repo
// value wins when it disagrees with global.
func TestCursorCloudDelayedDuplicateStaysUncleanedUntilLaterEmptySweep(t *testing.T) {
	global24 := &config.Config{CloudLaunchCleanDelayHours: 24}
	t.Run("later empty sweep", func(t *testing.T) {
		runDelayedDup(t, "", nil, 25*time.Hour, 24*time.Hour, true)
	})
	t.Run("repo zero", func(t *testing.T) {
		runDelayedDup(t, "cloud_launch_clean_delay_hours = 0\n", global24, 30*time.Minute, time.Hour, false)
	})
	t.Run("repo negative", func(t *testing.T) {
		runDelayedDup(t, "cloud_launch_clean_delay_hours = -3\n", global24, 30*time.Minute, time.Hour, false)
	})
	t.Run("repo unparseable", func(t *testing.T) {
		runDelayedDup(t, "cloud_launch_clean_delay_hours = \"soon\"\n", global24, 30*time.Minute, time.Hour, false)
	})
	t.Run("repo wins over global", func(t *testing.T) {
		runDelayedDup(t, "cloud_launch_clean_delay_hours = 2\n", global24, 3*time.Hour, 2*time.Hour, true)
	})
}

func runDelayedDup(t *testing.T, repoTOML string, global *config.Config, age, wantDelay time.Duration, wantLaterClean bool) {
	t.Helper()
	tc, _ := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	fake := &cursorcloudtest.Fake{CreateStatus: cursorcloud.StatusFinished, Final: "ok"}
	script := &cloudCreateScript{Fake: fake, extra: 1}
	client := &hideOnFirstList{Client: script, hideID: "bc_fake_2"}
	text, err := newBoundCloud(t, tc, job, client).Review(context.Background(), tc.TmpDir, sha, "prompt", nil)
	require.NoError(t, err)
	assert.Equal(t, "ok", text)
	_, err = fake.GetAgent(t.Context(), "bc_fake_1")
	require.Error(t, err)
	got, err := fake.GetAgent(t.Context(), "bc_fake_2")
	require.NoError(t, err)
	assert.Equal(t, "bc_fake_2", got.ID)
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, storage.CloudLaunchPendingNote, cursorCloudLaunchNote(t, tc.DB, job.ID))
	var jobErr string
	require.NoError(t, tc.DB.QueryRow(`SELECT COALESCE(error, '') FROM review_jobs WHERE id = ?`, job.ID).Scan(&jobErr))
	assert.Contains(t, jobErr, "orphan risk")

	if repoTOML != "" {
		require.NoError(t, os.WriteFile(filepath.Join(tc.TmpDir, ".roborev.toml"), []byte(repoTOML), 0o644))
	}
	assert.Equal(t, wantDelay, config.ResolveCloudLaunchCleanDelay(tc.TmpDir, global))
	cursorCloudAgeLaunchBy(t, tc.DB, job.ID, age)

	require.NoError(t, recoverLaunchSweep(tc.DB, client, global))
	_, err = fake.GetAgent(t.Context(), "bc_fake_2")
	require.Error(t, err)
	n, err = tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	assert.Equal(t, storage.CloudLaunchPendingNote, cursorCloudLaunchNote(t, tc.DB, job.ID))

	err = recoverLaunchSweep(tc.DB, client, global)
	n, nerr := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, nerr)
	require.ErrorContains(t, err, "not listed yet")
	assert.Equal(t, 1, n)
	assert.Equal(t, storage.CloudLaunchPendingNote, cursorCloudLaunchNote(t, tc.DB, job.ID))
	if !wantLaterClean {
		return
	}
	require.NoError(t, recoverLaunchSweep(tc.DB, client, global))
	n, err = tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
	assert.Empty(t, cursorCloudLaunchNote(t, tc.DB, job.ID))
}

// TestSavedIDMissDoesNotCleanWithoutPersistedNote is a saved id whose list
// finds nothing. The separate sweep tick is already recorded. Cleaning now
// would drop the row with no persisted orphan note. The pass writes the
// note and leaves the row uncleaned.
func TestSavedIDMissDoesNotCleanWithoutPersistedNote(t *testing.T) {
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	const token = "launch-token-saved-miss-note"
	cursorCloudBeginToken(t, tc, job.ID, token)
	require.NoError(t, tc.DB.AttachCloudAgent(job.ID, token, "bc_saved_miss_note"))
	cursorCloudAgeLaunchPastLag(t, tc.DB, job.ID)
	_, err := tc.DB.Exec(`UPDATE review_job_cloud_launches SET sweep_seen = 1 WHERE job_id = ?`, job.ID)
	require.NoError(t, err)
	fake.MethodErrors = map[string]error{
		cursorcloudtest.MethodDelete: &cursorcloud.APIError{
			Op: "delete agent", StatusCode: http.StatusNotFound, Message: "not found",
		},
	}

	_ = recoverStaleJobs(context.Background(), tc.DB, fake)
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var jobErr string
	require.NoError(t, tc.DB.QueryRow(`SELECT COALESCE(error, '') FROM review_jobs WHERE id = ?`, job.ID).Scan(&jobErr))
	assert.Contains(t, jobErr, "orphan risk")
}

func recoverLaunchSweep(db *storage.DB, client cursorcloud.Client, global *config.Config) error {
	if err := db.ResetStaleJobs(); err != nil {
		return err
	}
	return recoverCloudLaunches(context.Background(), db, client, global)
}

func cursorCloudAgeLaunchBy(t *testing.T, db *storage.DB, jobID int64, age time.Duration) {
	t.Helper()
	secs := max(int(age.Seconds()), 1)
	_, err := db.Exec(
		`UPDATE review_job_cloud_launches SET created_at = datetime('now', ?) WHERE job_id = ?`,
		fmt.Sprintf("-%d seconds", secs), jobID,
	)
	require.NoError(t, err)
}

// TestKnownIDFirstTickDoesNotCleanWhileMarkerPresent is the known-id
// delete. Another VM still carries this launch's marker. Deleting the
// saved id and cleaning on that same tick would leave the marker behind.
// The tick sweeps the marker and does not clean.
func TestKnownIDFirstTickDoesNotCleanWhileMarkerPresent(t *testing.T) {
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	const token = "launch-token-known-id-marker"
	machineID, err := tc.DB.GetMachineID()
	require.NoError(t, err)
	marker := agent.CursorCloudLaunchMarker(machineID.String(), token) //nolint:forbidigo // launch marker is prompt text
	saved := cursorCloudSeedAgent(t, fake, sha, marker)
	other := cursorCloudSeedAgent(t, fake, sha, marker)
	require.NoError(t, tc.DB.BeginCloudLaunch(job.ID, testWorkerID, token, cursorCloudRestartRepo))
	require.NoError(t, tc.DB.AttachCloudAgent(job.ID, token, saved.ID))
	cursorCloudAgeLaunchPastLag(t, tc.DB, job.ID)
	_, err = tc.DB.Exec(`UPDATE review_job_cloud_launches SET sweep_seen = 1 WHERE job_id = ?`, job.ID)
	require.NoError(t, err)
	_, err = tc.DB.Exec(
		`UPDATE review_jobs SET status = 'failed', error = ? WHERE id = ?`,
		agent.CursorCloudPendingOrphanNote(), job.ID,
	)
	require.NoError(t, err)

	require.NoError(t, recoverStaleJobs(context.Background(), tc.DB, fake))
	_, err = fake.GetAgent(t.Context(), saved.ID)
	require.Error(t, err)
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	_, err = fake.GetAgent(t.Context(), other.ID)
	require.Error(t, err)
	assert.Equal(t, storage.CloudLaunchPendingNote, cursorCloudLaunchNote(t, tc.DB, job.ID))
}

// TestReviewVerifierFirstEmptyAfterFoundDoesNotClean is the hidden-duplicate
// pin. It was not on any remote branch. Sweep 1 lists and deletes the visible
// VM. Sweep 2 lists nothing while a second VM with the same marker is still
// live and hidden from that list. The launch stays uncleaned: one empty list
// after a found one is not a confirmed delete and is not the later empty
// confirmation. This is the c10h delayed-duplicate hole.
func TestReviewVerifierFirstEmptyAfterFoundDoesNotClean(t *testing.T) {
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	const token = "launch-token-hidden-dup"
	machineID, err := tc.DB.GetMachineID()
	require.NoError(t, err)
	marker := agent.CursorCloudLaunchMarker(machineID.String(), token) //nolint:forbidigo // launch marker is prompt text
	visible := cursorCloudSeedAgent(t, fake, sha, marker)
	dup := cursorCloudSeedAgent(t, fake, sha, marker)
	require.NoError(t, tc.DB.BeginCloudLaunch(job.ID, testWorkerID, token, cursorCloudRestartRepo))
	require.NoError(t, tc.DB.AttachCloudAgent(job.ID, token, "bc_saved_gone"))
	cursorCloudAgeLaunchPastLag(t, tc.DB, job.ID)
	client := &firstListThenHide{Client: fake, hideID: dup.ID, savedID: "bc_saved_gone"}

	require.NoError(t, recoverLaunchSweep(tc.DB, client, nil))
	_, err = fake.GetAgent(t.Context(), visible.ID)
	require.Error(t, err)
	got, err := fake.GetAgent(t.Context(), dup.ID)
	require.NoError(t, err)
	assert.Equal(t, dup.ID, got.ID)
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	err = recoverLaunchSweep(tc.DB, client, nil)
	n, nerr := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, nerr)
	require.Error(t, err)
	assert.Equal(t, 1, n)
	assert.GreaterOrEqual(t, client.lists, 2)
	got, err = fake.GetAgent(t.Context(), dup.ID)
	require.NoError(t, err)
	assert.Equal(t, dup.ID, got.ID)
	assert.Equal(t, storage.CloudLaunchPendingNote, cursorCloudLaunchNote(t, tc.DB, job.ID))
}

// firstListThenHide shows every agent except hideID on the first list and an
// empty list after that. hideID stays in the fake. savedID answers 404 so the
// id delete succeeds without removing the hidden duplicate.
type firstListThenHide struct {
	cursorcloud.Client
	hideID  string
	savedID string
	lists   int
}

func (h *firstListThenHide) ListAgents(ctx context.Context) ([]cursorcloud.Agent, error) {
	h.lists++
	if h.lists > 1 {
		return nil, nil
	}
	agents, err := h.Client.ListAgents(ctx)
	if err != nil {
		return nil, err
	}
	visible := make([]cursorcloud.Agent, 0, len(agents))
	for _, ag := range agents {
		if ag.ID != h.hideID {
			visible = append(visible, ag)
		}
	}
	return visible, nil
}

func (h *firstListThenHide) StopAgent(ctx context.Context, id string) error {
	if id == h.savedID {
		return &cursorcloud.APIError{Op: "stop agent", StatusCode: http.StatusNotFound, Message: "not found"}
	}
	return h.Client.StopAgent(ctx, id)
}

func (h *firstListThenHide) DeleteAgent(ctx context.Context, id string) error {
	if id == h.savedID {
		return &cursorcloud.APIError{Op: "delete agent", StatusCode: http.StatusNotFound, Message: "not found"}
	}
	return h.Client.DeleteAgent(ctx, id)
}

// TestCloudLaunchExact24hMaxAgeBoundary kills the M4 mutant that changes
// the 24h comparison from >= to >. At created+24h the empty confirmation
// cleans, and a token-only miss writes the age-out note. One nanosecond
// short of that instant does neither. Past-24h tests stay green under M4.
func TestCloudLaunchExact24hMaxAgeBoundary(t *testing.T) {
	const layout = "2006-01-02 15:04:05"
	now := time.Date(2026, 10, 20, 16, 0, 0, 0, time.UTC)
	exact := now.Add(-24 * time.Hour).Format(layout)
	young := now.Add(-24 * time.Hour).Add(time.Second).Format(layout)

	t.Run("exact cleans", func(t *testing.T) {
		n := runBoundarySweep(t, now, exact)
		assert.Equal(t, 0, n)
	})
	t.Run("one second young stays", func(t *testing.T) {
		n := runBoundarySweep(t, now, young)
		assert.Equal(t, 1, n)
	})
	t.Run("exact age-out note", func(t *testing.T) {
		text := runBoundaryNote(t, now, exact)
		assert.Contains(t, text, "was not listed within cloud_launch_clean_delay_hours (24h0m0s)")
	})
	t.Run("one second young skips age-out", func(t *testing.T) {
		text := runBoundaryNote(t, now, young)
		assert.NotContains(t, text, "was not listed within")
		assert.Contains(t, text, "orphan risk")
	})
}

func runBoundarySweep(t *testing.T, now time.Time, created string) int {
	t.Helper()
	restore := agent.SetCursorCloudClockForTest(func() time.Time { return now })
	t.Cleanup(restore)
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	const token = "launch-token-exact-24h"
	cursorCloudBeginToken(t, tc, job.ID, token)
	require.NoError(t, tc.DB.AttachCloudAgent(job.ID, token, "bc_boundary"))
	fake.MethodErrors = map[string]error{
		cursorcloudtest.MethodDelete: &cursorcloud.APIError{
			Op: "delete agent", StatusCode: http.StatusNotFound, Message: "not found",
		},
	}
	_, err := tc.DB.Exec(`UPDATE review_job_cloud_launches SET created_at = ? WHERE job_id = ?`, created, job.ID)
	require.NoError(t, err)

	// The restart that fails the running job writes the interrupted error
	// over any note, so the first empty list cannot be the confirmation.
	require.ErrorContains(t, recoverLaunchSweep(tc.DB, fake, nil), "not listed yet")
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	err = recoverLaunchSweep(tc.DB, fake, nil)
	n, nerr := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, nerr)
	if n == 0 {
		require.NoError(t, err)
	}
	return n
}

func runBoundaryNote(t *testing.T, now time.Time, created string) string {
	t.Helper()
	restore := agent.SetCursorCloudClockForTest(func() time.Time { return now })
	t.Cleanup(restore)
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	cursorCloudBeginToken(t, tc, job.ID, "launch-token-exact-24h-note")
	_, err := tc.DB.Exec(`UPDATE review_job_cloud_launches SET created_at = ? WHERE job_id = ?`, created, job.ID)
	require.NoError(t, err)

	_ = recoverLaunchSweep(tc.DB, fake, nil)
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var text string
	require.NoError(t, tc.DB.QueryRow(`SELECT COALESCE(error, '') FROM review_jobs WHERE id = ?`, job.ID).Scan(&text))
	return text
}

// TestSamePassListsEmptyTwiceDoesNotClean is one restart that lists empty
// twice. The second list is not a later restart, so the row stays uncleaned.
// The next restart's empty list cleans it.
func TestSamePassListsEmptyTwiceDoesNotClean(t *testing.T) {
	tc, fake := cursorCloudRestartFixture(t)
	sha := testutil.GetHeadSHA(t, tc.TmpDir)
	job := tc.createAndClaimJobWithAgent(t, sha, testWorkerID, agent.CursorCloudHost)
	const token = "launch-token-two-empty-lists"
	cursorCloudBeginToken(t, tc, job.ID, token)
	require.NoError(t, tc.DB.AttachCloudAgent(job.ID, token, "bc_already_gone"))
	cursorCloudAgeLaunchPastLag(t, tc.DB, job.ID)
	fake.MethodErrors = map[string]error{
		cursorcloudtest.MethodDelete: &cursorcloud.APIError{
			Op: "delete agent", StatusCode: http.StatusNotFound, Message: "not found",
		},
	}
	prev := cursorCloudSamePassResweep
	cursorCloudSamePassResweep = func(ctx context.Context, client cursorcloud.Client, daemonID, sweepToken, repoURL string) (bool, error) {
		return agent.DeleteCursorCloudLaunchByMarker(ctx, client, daemonID, sweepToken, repoURL)
	}
	t.Cleanup(func() { cursorCloudSamePassResweep = prev })

	require.ErrorContains(t, recoverStaleJobs(context.Background(), tc.DB, fake), "not listed yet")
	assert.GreaterOrEqual(t, cursorCloudCallCount(fake, cursorcloudtest.MethodList), 2)
	n, err := tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
	var jobErr string
	require.NoError(t, tc.DB.QueryRow(`SELECT COALESCE(error, '') FROM review_jobs WHERE id = ?`, job.ID).Scan(&jobErr))
	assert.Contains(t, jobErr, "orphan risk")

	cursorCloudSamePassResweep = nil
	require.NoError(t, recoverStaleJobs(context.Background(), tc.DB, fake))
	n, err = tc.DB.CountUncleanedCloudLaunches(job.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, n)
}
