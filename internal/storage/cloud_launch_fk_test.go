package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudLaunchRebuildDropsCascadeAndKeepsRows(t *testing.T) {
	t.Parallel()
	for _, fkOn := range []bool{true, false} {
		name := "fk off"
		if fkOn {
			name = "fk on"
		}
		t.Run("job delete "+name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
			t.Cleanup(func() { db.Close() })
			row := seedCascadeLaunch(t, db, "/tmp/fk-job-"+name)
			require.NoError(t, deleteReviewJobFK(t, db, row.jobID, fkOn))
			assertLaunchKept(t, db, row)
		})
		t.Run("repo delete "+name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
			t.Cleanup(func() { db.Close() })
			row := seedCascadeLaunch(t, db, "/tmp/fk-repo-"+name)
			db.SetMaxOpenConns(1)
			setDBForeignKeys(t, db, fkOn)
			require.NoError(t, db.DeleteRepo(row.repoID, true))
			assertLaunchKept(t, db, row)
		})
	}
}

type cascadeLaunch struct {
	id     int64
	repoID int64
	jobID  int64
	token  string
	note   string
	when   string
}

func seedCascadeLaunch(t *testing.T, db *DB, repoPath string) cascadeLaunch {
	t.Helper()
	repo, _, job := createJobChain(t, db, repoPath, "abc123")
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
	const token = "fk-keep-tok"
	const note = "cursor-cloud orphan risk: keep"
	const when = "2000-01-01 00:00:00"
	res, err := db.Exec(`INSERT INTO review_job_cloud_launches
		(job_id, launch_token, agent_id, cleaned, repo_identity, created_at, orphan_note)
		VALUES (?, ?, 'bc_keep', 0, 'https://example.com/acme/old.git', ?, ?)`,
		job.ID, token, when, note)
	require.NoError(t, err)
	id, err := res.LastInsertId()
	require.NoError(t, err)
	require.NoError(t, db.ensureCloudLaunchTable())
	return cascadeLaunch{id: id, repoID: repo.ID, jobID: job.ID, token: token, note: note, when: when}
}

func assertLaunchKept(t *testing.T, db *DB, row cascadeLaunch) {
	t.Helper()
	var id, cleaned int64
	var token, note, when, agentID string
	require.NoError(t, db.QueryRow(`SELECT id, launch_token, agent_id, cleaned, created_at, orphan_note
		FROM review_job_cloud_launches WHERE launch_token = ?`, row.token).Scan(
		&id, &token, &agentID, &cleaned, &when, &note))
	assert.Equal(t, row.id, id)
	assert.Equal(t, row.token, token)
	assert.Equal(t, "bc_keep", agentID)
	assert.Equal(t, int64(0), cleaned)
	assert.Equal(t, row.when, when)
	assert.Equal(t, row.note, note)
	var ddl string
	require.NoError(t, db.QueryRow(`SELECT sql FROM sqlite_master WHERE name = 'review_job_cloud_launches'`).Scan(&ddl))
	require.NotContains(t, ddl, "REFERENCES")
	require.NotContains(t, ddl, "ON DELETE CASCADE")
	var fks int
	require.NoError(t, db.QueryRow(`SELECT COUNT(*) FROM pragma_foreign_key_list('review_job_cloud_launches')`).Scan(&fks))
	require.Equal(t, 0, fks)
}

func setDBForeignKeys(t *testing.T, db *DB, on bool) {
	t.Helper()
	conn, err := db.Conn(t.Context())
	require.NoError(t, err)
	stmt := `PRAGMA foreign_keys = OFF`
	if on {
		stmt = `PRAGMA foreign_keys = ON`
	}
	_, err = conn.ExecContext(t.Context(), stmt)
	require.NoError(t, err)
	require.NoError(t, conn.Close())
}

func deleteReviewJobFK(t *testing.T, db *DB, jobID int64, fkOn bool) error {
	t.Helper()
	db.SetMaxOpenConns(1)
	conn, err := db.Conn(t.Context())
	if err != nil {
		return err
	}
	defer conn.Close()
	stmt := `PRAGMA foreign_keys = OFF`
	if fkOn {
		stmt = `PRAGMA foreign_keys = ON`
	}
	if _, err = conn.ExecContext(t.Context(), stmt); err != nil {
		return err
	}
	_, err = conn.ExecContext(t.Context(), `DELETE FROM review_jobs WHERE id = ?`, jobID)
	return err
}
