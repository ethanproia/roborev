package storage

import (
	"database/sql"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shapes where SQLite datetime() and ParseCloudLaunchCreatedAt disagree, plus
// the values neither accepts. want is the text Open must store. A now-rewrite
// replaces that text with the current clock, so the age check fails.
func TestCloudLaunchCreatedAtBackfillShapes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		raw     string
		null    bool
		want    string
		wantNow bool
	}{
		{name: "null", null: true, want: " "},
		{name: "empty", raw: "", wantNow: true},
		{name: "whitespace", raw: " \t ", want: " \t "},
		{name: "leading-space", raw: " 1999-06-15 12:00:00", want: " 1999-06-15 12:00:00"},
		{name: "trailing-space", raw: "1999-06-15 12:00:00 ", want: "1999-06-15 12:00:00"},
		{name: "trailing-newline", raw: "1999-06-15 12:00:00\n", want: "1999-06-15 12:00:00"},
		{name: "garbage", raw: "not-a-date", want: "not-a-date"},
		{name: "unix-seconds", raw: "946684800", want: "946684800"},
		{name: "epoch", raw: "0", want: "-4713-11-24 12:00:00"},
		{name: "julian-unix", raw: "2440587.5", want: "1970-01-01 00:00:00"},
		{name: "julian-day", raw: "2451545", want: "2000-01-01 12:00:00"},
		{name: "plus-0000", raw: "+0000", want: "-4713-11-24 12:00:00"},
		{name: "date-only", raw: "1999-06-15", want: "1999-06-15 00:00:00"},
		{name: "day-overflow", raw: "1999-06-31", want: "1999-07-01 00:00:00"},
		{name: "non-leap", raw: "1900-02-29", want: "1900-03-01 00:00:00"},
		{name: "minute-precision", raw: "1999-06-15 12:00", want: "1999-06-15 12:00:00"},
		{name: "minute-t", raw: "1999-06-15T12:00", want: "1999-06-15 12:00:00"},
		{name: "minute-z", raw: "1999-06-15T12:00Z", want: "1999-06-15 12:00:00"},
		{name: "minute-offset", raw: "1999-06-15T12:00-05:00", want: "1999-06-15 17:00:00"},
		{name: "t-without-zone", raw: "1999-06-15T12:00:00", want: "1999-06-15 12:00:00"},
		{name: "t-fractional", raw: "1999-06-15T12:00:00.500", want: "1999-06-15 12:00:00"},
		{name: "lowercase-z", raw: "1999-06-15T12:00:00z", want: "1999-06-15 12:00:00"},
		{name: "space-lowercase-z", raw: "1999-06-15 12:00:00z", want: "1999-06-15 12:00:00"},
		{name: "space-with-z", raw: "1999-06-15 12:00:00Z", want: "1999-06-15 12:00:00"},
		{name: "space-offset", raw: "1999-06-15 12:00:00-05:00", want: "1999-06-15 17:00:00"},
		{name: "space-fractional-z", raw: "1999-06-15 12:00:00.500Z", want: "1999-06-15 12:00:00"},
		{name: "space-fractional-offset", raw: "1999-06-15 12:00:00.5-05:00", want: "1999-06-15 17:00:00"},
		{name: "trailing-z-space", raw: "1999-06-15T12:00:00Z ", want: "1999-06-15 12:00:00"},
		{name: "time-only", raw: "12:00:00", want: "2000-01-01 12:00:00"},
		{name: "time-only-hm", raw: "12:00", want: "2000-01-01 12:00:00"},
		{name: "time-only-fractional", raw: "12:00:00.5", want: "2000-01-01 12:00:00"},
		{name: "hour-24", raw: "1999-06-15 24:00:00", want: "1999-06-15 24:00:00"},
		{name: "hour-24-z", raw: "1999-06-15T24:00:00Z", want: "1999-06-15 24:00:00"},
		{name: "keyword-now", raw: "now", want: "now"},
		{name: "keyword-NOW", raw: "NOW", want: "NOW"},
		{name: "fractional-seconds", raw: "1999-06-15 12:00:00.500", want: "1999-06-15 12:00:00"},
		{name: "zulu", raw: "1999-06-15T12:00:00Z", want: "1999-06-15 12:00:00"},
		{name: "utc-offset", raw: "1999-06-15T12:00:00+00:00", want: "1999-06-15 12:00:00"},
		{name: "non-utc-offset", raw: "1999-06-15T12:00:00-05:00", want: "1999-06-15 17:00:00"},
		{name: "half-hour-offset", raw: "1999-06-15T12:00:00+05:30", want: "1999-06-15 06:30:00"},
		{name: "double-space", raw: "1999-06-15  12:00:00", want: "1999-06-15 12:00:00"},
		{name: "comma-fractional", raw: "1999-06-15 12:00:00,500", want: "1999-06-15 12:00:00,500"},
		{name: "negative-year", raw: "-4713-11-24 12:00:00", want: "-4713-11-24 12:00:00"},
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
			if tc.wantNow {
				parsed, ok := ParseCloudLaunchCreatedAt(got.String)
				require.True(t, ok, "created_at %q does not parse", got.String)
				assert.Less(t, time.Since(parsed).Abs(), 2*time.Minute)
				return
			}
			assert.Equal(t, tc.want, got.String)
			parsed, ok := ParseCloudLaunchCreatedAt(got.String)
			if ok {
				assert.False(t, parsed.Add(24*time.Hour).After(time.Now().UTC()), "age clock was reset to %s", got.String)
			}
		})
	}
}

func nullableCreatedAt(isNull bool, raw string) any {
	if isNull {
		return nil
	}
	return raw
}
