package tui

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/roborev/internal/storage"
	"go.kenn.io/roborev/internal/testutil"
)

func TestDetailPaneShowsLaunchOrphanWarning(t *testing.T) {
	t.Parallel()
	note := storage.CloudLaunchPendingNote
	finished := splitTestFinishedAt

	cases := []struct {
		name   string
		status storage.JobStatus
		idx    int
		jobID  int64
		review bool
	}{
		{name: "done", status: storage.JobStatusDone, idx: 1, jobID: 2, review: true},
		{name: "failed", status: storage.JobStatusFailed, idx: 2, jobID: 1, review: true},
		{name: "failed without review", status: storage.JobStatusFailed, idx: 2, jobID: 1},
		{name: "canceled", status: storage.JobStatusCanceled, idx: 0, jobID: 3},
		{name: "running", status: storage.JobStatusRunning, idx: 0, jobID: 3},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := splitModel(withSelection(tc.idx, tc.jobID))
			m.currentReview = nil
			require.Greater(t, len(m.jobs), tc.idx)
			m.jobs[tc.idx].Status = tc.status
			m.jobs[tc.idx].Error = "agent crashed"
			m.jobs[tc.idx].OrphanWarning = note
			if tc.status == storage.JobStatusDone || tc.status == storage.JobStatusFailed {
				m.jobs[tc.idx].FinishedAt = &finished
			}
			if tc.review {
				job := m.jobs[tc.idx]
				m.currentReview = &storage.Review{
					VerdictBool: testutil.ReviewFixtureVerdict("loaded review body"),
					ID:          10, JobID: job.ID, Agent: job.Agent,
					Output: "loaded review body",
					Job:    &job,
				}
				require.True(t, m.selectedReviewLoaded())
			}

			joined := stripANSI(strings.Join(m.renderDetailPane(88, 25), "\n"))
			assert.Contains(t, joined, "Warning: "+note)
			assert.NotContains(t, m.jobs[tc.idx].Error, note)
			if tc.review {
				assert.Contains(t, joined, "loaded review body")
			}
		})
	}
}

func TestDetailPaneOmitsLaunchWarningWhenNoteIsEmpty(t *testing.T) {
	t.Parallel()
	m := splitModel(withSelection(0, 3))
	m.currentReview = nil
	m.jobs[0].OrphanWarning = "  "
	joined := stripANSI(strings.Join(m.renderDetailPane(88, 25), "\n"))
	assert.NotContains(t, joined, "Warning:")
	assert.Contains(t, joined, "running")
}

func TestDetailPaneSanitizesLaunchOrphanWarning(t *testing.T) {
	t.Parallel()
	m := splitModel(withSelection(0, 3))
	m.currentReview = nil
	m.jobs[0].OrphanWarning = "cursor-cloud orphan risk: \x1b]52;c;evil\x07launch"
	joined := strings.Join(m.renderDetailPane(88, 25), "\n")
	assert.Contains(t, stripANSI(joined), "Warning: cursor-cloud orphan risk: launch")
	assert.NotContains(t, joined, "\x1b]52")
}
