package storage

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shapes where SQLite datetime() and ParseCloudLaunchCreatedAt disagree, plus
// the values neither accepts. canonical is datetime()'s text when it returns
// one; otherwise the raw value must be left unchanged (never datetime('now')).
func TestCloudLaunchCreatedAtBackfillShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		raw       string
		null      bool
		canonical string
		keepRaw   bool
	}{
		{name: "null", null: true, canonical: " "},
		{name: "empty", raw: "", keepRaw: true},
		{name: "whitespace", raw: " \t ", keepRaw: true},
		{name: "leading-space", raw: " 1999-06-15 12:00:00", keepRaw: true},
		{name: "trailing-space", raw: "1999-06-15 12:00:00 ", canonical: "1999-06-15 12:00:00"},
		{name: "garbage", raw: "not-a-date", keepRaw: true},
		{name: "epoch", raw: "0", canonical: "-4713-11-24 12:00:00"},
		{name: "unix-seconds", raw: "946684800", keepRaw: true},
		{name: "date-only", raw: "1999-06-15", canonical: "1999-06-15 00:00:00"},
		{name: "minute-precision", raw: "1999-06-15 12:00", canonical: "1999-06-15 12:00:00"},
		{name: "t-without-zone", raw: "1999-06-15T12:00:00", canonical: "1999-06-15 12:00:00"},
		{name: "fractional-seconds", raw: "1999-06-15 12:00:00.500", canonical: "1999-06-15 12:00:00"},
		{name: "zulu", raw: "1999-06-15T12:00:00Z", canonical: "1999-06-15 12:00:00"},
		{name: "utc-offset", raw: "1999-06-15T12:00:00+00:00", canonical: "1999-06-15 12:00:00"},
		{name: "non-utc-offset", raw: "1999-06-15T12:00:00-05:00", canonical: "1999-06-15 17:00:00"},
		{name: "space-with-z", raw: "1999-06-15 12:00:00Z", canonical: "1999-06-15 12:00:00"},
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
				created_at TEXT,
				orphan_note TEXT NOT NULL DEFAULT '')`)
			require.NoError(t, err)
			_, err = db.Exec(`INSERT INTO review_job_cloud_launches (job_id, launch_token, created_at)
				VALUES (1, ?, ?)`, "shape-"+tc.name, nullableCreatedAt(tc.null, tc.raw))
			require.NoError(t, err)

			require.NoError(t, db.ensureCloudLaunchTable())

			var got sql.NullString
			require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = ?`, "shape-"+tc.name).Scan(&got))
			require.True(t, got.Valid)
			if tc.keepRaw {
				assert.Equal(t, tc.raw, got.String)
				_, ok := ParseCloudLaunchCreatedAt(got.String)
				assert.False(t, ok)
				return
			}
			assert.Equal(t, tc.canonical, got.String)
			parsed, ok := ParseCloudLaunchCreatedAt(got.String)
			require.True(t, ok, "created_at %q does not parse", got.String)
			assert.True(t, !parsed.Add(24*time.Hour).After(time.Now().UTC()), "age clock was reset to %s", got.String)
		})
	}
}

func nullableCreatedAt(isNull bool, raw string) any {
	if isNull {
		return nil
	}
	return raw
}
