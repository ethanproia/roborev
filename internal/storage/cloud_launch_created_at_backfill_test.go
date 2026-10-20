package storage

import (
	"regexp"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCloudLaunchCreatedAtBackfillShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		raw       string
		canonical string
	}{
		{name: "empty", raw: ""},
		{name: "whitespace", raw: " \t "},
		{name: "garbage", raw: "not-a-date"},
		{name: "rfc3339-offset", raw: "1999-06-15T12:00:00-05:00", canonical: "1999-06-15 17:00:00"},
		{name: "unix-seconds", raw: "946684800"},
		{name: "date-only", raw: "1999-06-15"},
		{name: "sqlite-t-without-zone", raw: "1999-06-15T12:00:00"},
		{name: "sqlite-space-with-z", raw: "1999-06-15 12:00:00Z"},
		{name: "sqlite-datetime", raw: "1999-06-15 17:00:00", canonical: "1999-06-15 17:00:00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			db := openTestDB(t)
			t.Cleanup(func() { db.Close() })
			_, err := db.Exec(`DROP TABLE review_job_cloud_launches`)
			require.NoError(t, err)
			_, err = db.Exec(`CREATE TABLE review_job_cloud_launches (
				id INTEGER PRIMARY KEY,
				job_id INTEGER,
				launch_token TEXT NOT NULL,
				agent_id TEXT NOT NULL DEFAULT '',
				cleaned INTEGER NOT NULL DEFAULT 0,
				repo_identity TEXT NOT NULL DEFAULT '',
				created_at TEXT NOT NULL DEFAULT '',
				orphan_note TEXT NOT NULL DEFAULT '')`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO review_job_cloud_launches (job_id, launch_token, created_at)
				VALUES (1, ?, ?)`, "shape-"+tc.name, tc.raw)
			require.NoError(t, err)

			require.NoError(t, db.ensureCloudLaunchTable())

			var got string
			require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = ?`, "shape-"+tc.name).Scan(&got))
			parsed, ok := ParseCloudLaunchCreatedAt(got)
			require.True(t, ok, "created_at %q does not parse", got)
			assert.Regexp(t, sqliteCreatedAtPattern, got)
			aged := !parsed.Add(24 * time.Hour).After(time.Now().UTC())
			if tc.canonical != "" {
				assert.Equal(t, tc.canonical, got)
				assert.True(t, aged)
				return
			}
			assert.NotEqual(t, tc.raw, got)
			assert.False(t, aged)
		})
	}
}

var sqliteCreatedAtPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2} \d{2}:\d{2}:\d{2}$`)
