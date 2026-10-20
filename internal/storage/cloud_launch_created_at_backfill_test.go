package storage

import (
	"context"
	"database/sql"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// sqliteDatetimeText is the text datetime('now') and CURRENT_TIMESTAMP render.
var sqliteDatetimeText = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}$`)

// pgTimestamptzText is Postgres DateStyle ISO text for timestamptz.
// NOW() keeps microseconds; a whole second omits the fraction. The offset
// has no colon.
var pgTimestamptzText = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}(\.[0-9]{1,6})?[+-][0-9]{2}$`)

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
		current bool
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
		// Any other case fold of now is the same EqualFold skip.
		{name: "keyword-Now", raw: "Now", want: "Now"},
		{name: "keyword-NoW", raw: "NoW", want: "NoW"},
		{name: "keyword-nOw", raw: "nOw", want: "nOw"},
		// datetime('now') output. Already canonical, so Open stores it unchanged.
		{name: "sqlite-datetime", raw: "1999-06-15 17:00:00", want: "1999-06-15 17:00:00"},
		// Same second-precision text ParseCloudLaunchCreatedAt's sqlite layout accepts.
		{name: "sqlite-seconds", raw: "1999-06-15 17:00:00", want: "1999-06-15 17:00:00"},
		// Fractional RFC3339. A dot with Z or an offset is a different datetime()
		// text, so dropping the rewrite stores the raw value and the cell fails.
		{name: "frac-dot-z", raw: "1999-06-15T12:00:00.500Z", want: "1999-06-15 12:00:00"},
		{name: "frac-dot-z-short", raw: "1999-06-15T12:00:00.5Z", want: "1999-06-15 12:00:00"},
		{name: "frac-dot-offset", raw: "1999-06-15T12:00:00.500-05:00", want: "1999-06-15 17:00:00"},
		{name: "frac-dot-utc", raw: "1999-06-15T12:00:00.5+00:00", want: "1999-06-15 12:00:00"},
		{name: "frac-dot-half", raw: "1999-06-15T12:00:00.5+05:30", want: "1999-06-15 06:30:00"},
		// A comma is a fractional second for Go and not for datetime(), so the text stays.
		{name: "frac-comma-z", raw: "1999-06-15T12:00:00,500Z", want: "1999-06-15T12:00:00,500Z"},
		{name: "frac-comma-offset", raw: "1999-06-15T12:00:00,500-05:00", want: "1999-06-15T12:00:00,500-05:00"},
		{name: "frac-comma-utc", raw: "1999-06-15T12:00:00,5+00:00", want: "1999-06-15T12:00:00,5+00:00"},
		{name: "offset-minus-zero", raw: "1999-06-15T12:00:00-00:00", want: "1999-06-15 12:00:00"},
		{name: "offset-minus-zero-frac", raw: "1999-06-15T12:00:00.5-00:00", want: "1999-06-15 12:00:00"},
		{name: "offset-minus-zero-comma", raw: "1999-06-15T12:00:00,5-00:00", want: "1999-06-15T12:00:00,5-00:00"},
		{name: "offset-minus-zero-space", raw: "1999-06-15 12:00:00-00:00", want: "1999-06-15 12:00:00"},
		{name: "offset-plus-14", raw: "1999-06-15T12:00:00+14:00", want: "1999-06-14 22:00:00"},
		{name: "offset-minus-12", raw: "1999-06-15T12:00:00-12:00", want: "1999-06-16 00:00:00"},
		{name: "offset-plus-14-midnight", raw: "1999-06-15T00:00:00+14:00", want: "1999-06-14 10:00:00"},
		{name: "offset-minus-12-late", raw: "1999-06-15T23:59:59-12:00", want: "1999-06-16 11:59:59"},
		{name: "offset-minus-13", raw: "1999-06-15T12:00:00-13:00", want: "1999-06-16 01:00:00"},
		{name: "frac-dot-plus-14", raw: "1999-06-15T12:00:00.5+14:00", want: "1999-06-14 22:00:00"},
		{name: "frac-dot-minus-12", raw: "1999-06-15T12:00:00.5-12:00", want: "1999-06-16 00:00:00"},
		// Go accepts +15:00. datetime() does not, so the text stays.
		{name: "offset-plus-15", raw: "1999-06-15T12:00:00+15:00", want: "1999-06-15T12:00:00+15:00"},
		// More than 9 fractional digits. datetime() keeps the whole second.
		{name: "sub-ns-z", raw: "1999-06-15T12:00:00.1234567890Z", want: "1999-06-15 12:00:00"},
		{name: "sub-ns-long", raw: "1999-06-15T12:00:00.1234567890123Z", want: "1999-06-15 12:00:00"},
		{name: "sub-ns-tiny", raw: "1999-06-15T12:00:00.0000000001Z", want: "1999-06-15 12:00:00"},
		{name: "sub-ns-offset", raw: "1999-06-15T12:00:00.1234567890123-05:00", want: "1999-06-15 17:00:00"},
		{name: "sub-ns-space", raw: "1999-06-15 12:00:00.1234567890123", want: "1999-06-15 12:00:00"},
		{name: "sub-ns-comma-z", raw: "1999-06-15T12:00:00,1234567890123Z", want: "1999-06-15T12:00:00,1234567890123Z"},
		// preciseTimestampAt layout. datetime() drops the fraction and zone.
		{name: "precise-timestamp", raw: "1999-06-15T12:00:00.000000000Z", want: "1999-06-15 12:00:00"},
		{name: "fractional-seconds", raw: "1999-06-15 12:00:00.500", want: "1999-06-15 12:00:00"},
		{name: "zulu", raw: "1999-06-15T12:00:00Z", want: "1999-06-15 12:00:00"},
		{name: "utc-offset", raw: "1999-06-15T12:00:00+00:00", want: "1999-06-15 12:00:00"},
		{name: "non-utc-offset", raw: "1999-06-15T12:00:00-05:00", want: "1999-06-15 17:00:00"},
		{name: "half-hour-offset", raw: "1999-06-15T12:00:00+05:30", want: "1999-06-15 06:30:00"},
		{name: "double-space", raw: "1999-06-15  12:00:00", want: "1999-06-15 12:00:00"},
		{name: "comma-fractional", raw: "1999-06-15 12:00:00,500", want: "1999-06-15 12:00:00,500"},
		{name: "negative-year", raw: "-4713-11-24 12:00:00", want: "-4713-11-24 12:00:00"},
		// CURRENT_TIMESTAMP rendered text. The subtest reads it from SQLite.
		{name: "current-timestamp", current: true},
		// Postgres TIMESTAMPTZ text (DateStyle ISO). datetime() rejects +00,
		// so Open leaves the text alone. Captured from Postgres 16:
		// timestamptz '1999-06-15 17:00:00+00' and a microsecond NOW() value.
		{name: "postgres-timestamptz", raw: "1999-06-15 17:00:00+00", want: "1999-06-15 17:00:00+00"},
		{name: "postgres-timestamptz-fraction", raw: "1999-06-15 17:00:00.344331+00", want: "1999-06-15 17:00:00.344331+00"},
	}
	assert.Equal(t, "1999-06-15T12:00:00.000000000Z", preciseTimestampAt(time.Date(1999, 6, 15, 12, 0, 0, 0, time.UTC)))
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
			raw := tc.raw
			want := tc.want
			if tc.current {
				require.NoError(t, db.QueryRow(`SELECT CURRENT_TIMESTAMP`).Scan(&raw))
				assert.Regexp(t, sqliteDatetimeText, raw)
				want = raw
			}
			_, err = db.Exec(`INSERT INTO review_job_cloud_launches (job_id, launch_token, created_at)
				VALUES (1, ?, ?)`, "shape-"+tc.name, nullableCreatedAt(tc.null, raw))
			require.NoError(t, err)

			require.NoError(t, db.ensureCloudLaunchTable())

			var got sql.NullString
			require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = ?`, "shape-"+tc.name).Scan(&got))
			require.True(t, got.Valid)
			if tc.wantNow || tc.current {
				parsed, ok := ParseCloudLaunchCreatedAt(got.String)
				require.True(t, ok, "created_at %q does not parse", got.String)
				assert.Less(t, time.Since(parsed).Abs(), 2*time.Minute)
				if tc.current {
					assert.Equal(t, want, got.String)
				}
				return
			}
			assert.Equal(t, want, got.String)
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

func openCloudLaunchShapeDB(t *testing.T) *DB {
	t.Helper()
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
	return db
}

// TestEmptyCreatedAtBecomesDatetimeNow pins the empty-string rewrite. Reverting
// the UPDATE that sets created_at to datetime('now') where created_at is empty
// leaves the stored text empty, and this test fails. A canonical datetime on
// another row stays put, so a blanket now-rewrite fails too.
func TestEmptyCreatedAtBecomesDatetimeNow(t *testing.T) {
	t.Parallel()
	db := openCloudLaunchShapeDB(t)
	_, err := db.Exec(`INSERT INTO review_job_cloud_launches (job_id, launch_token, created_at)
		VALUES (1, 'empty', ''), (1, 'kept', '1999-06-15 17:00:00')`)
	require.NoError(t, err)

	require.NoError(t, db.backfillCloudLaunchCreatedAt())

	var got string
	require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = 'empty'`).Scan(&got))
	assert.Regexp(t, sqliteDatetimeText, got)
	parsed, ok := ParseCloudLaunchCreatedAt(got)
	require.True(t, ok, "created_at %q does not parse", got)
	assert.Less(t, time.Since(parsed).Abs(), 2*time.Minute)

	var kept string
	require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = 'kept'`).Scan(&kept))
	assert.Equal(t, "1999-06-15 17:00:00", kept)
}

// TestBareNowCaseFoldIsLeftUnchanged pins the EqualFold("now") skip.
// datetime() of each spelling is the current clock, so deleting that skip
// stores the clock and these cases fail. The five spellings are separate
// subtests.
func TestBareNowCaseFoldIsLeftUnchanged(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"now", "NOW", "Now", "NoW", "nOw"} {
		t.Run(raw, func(t *testing.T) {
			t.Parallel()
			db := openCloudLaunchShapeDB(t)
			var clock string
			require.NoError(t, db.QueryRow(`SELECT datetime(?)`, raw).Scan(&clock))
			assert.Regexp(t, sqliteDatetimeText, clock)
			assert.NotEqual(t, raw, clock)

			_, err := db.Exec(`INSERT INTO review_job_cloud_launches (job_id, launch_token, created_at)
				VALUES (1, ?, ?)`, "now-"+raw, raw)
			require.NoError(t, err)
			require.NoError(t, db.backfillCloudLaunchCreatedAt())

			var got string
			require.NoError(t, db.QueryRow(`SELECT created_at FROM review_job_cloud_launches WHERE launch_token = ?`, "now-"+raw).Scan(&got))
			assert.Equal(t, raw, got)
		})
	}
}

// The postgres launch table stores timestamptz and has no sqlite backfill.
// ensureLocalCloudLaunchTable never calls backfillCloudLaunchCreatedAt.
func TestLocalCloudLaunchCreatedAtIsTimestamptzNow(t *testing.T) {
	t.Parallel()
	assert.Contains(t, localCloudLaunchDDL, "created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()")
	assert.NotContains(t, localCloudLaunchDDL, "datetime(")
	assert.NotContains(t, localCloudLaunchDDL, "backfillCloudLaunchCreatedAt")
}

func TestPostgresCloudLaunchCreatedAtSkipsSQLiteBackfill(t *testing.T) { //nolint:paralleltest // shares the roborev schema in the PostgreSQL database at TEST_POSTGRES_URL
	pool := openTestPgPool(t)
	ctx := t.Context()
	token := "pg-created-at-shape"
	t.Cleanup(func() {
		_, _ = pool.pool.Exec(context.Background(), `DELETE FROM review_job_cloud_launches WHERE launch_token = $1`, token)
	})
	_, err := pool.pool.Exec(ctx, `DELETE FROM review_job_cloud_launches WHERE launch_token = $1`, token)
	require.NoError(t, err)

	var dataType, nullable string
	var columnDefault sql.NullString
	err = pool.pool.QueryRow(ctx, `
		SELECT data_type, is_nullable, column_default
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'review_job_cloud_launches'
		  AND column_name = 'created_at'`).Scan(&dataType, &nullable, &columnDefault)
	require.NoError(t, err)
	assert.Equal(t, "timestamp with time zone", dataType)
	assert.Equal(t, "NO", nullable)
	require.True(t, columnDefault.Valid)
	assert.Contains(t, strings.ToLower(columnDefault.String), "now()")

	_, err = pool.pool.Exec(ctx, `INSERT INTO review_job_cloud_launches (launch_token) VALUES ($1)`, token)
	require.NoError(t, err)
	var created time.Time
	var text string
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT created_at, created_at::text FROM review_job_cloud_launches WHERE launch_token = $1`, token).Scan(&created, &text))
	assert.Less(t, time.Since(created).Abs(), 2*time.Minute)

	// A second ensure is the migration path. It must not run the sqlite
	// backfill, which would rewrite or reject this timestamptz value.
	pool.ensureLocalCloudLaunchTable(ctx)
	var textAgain string
	var dataTypeAgain string
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT created_at::text FROM review_job_cloud_launches WHERE launch_token = $1`, token).Scan(&textAgain))
	assert.Equal(t, text, textAgain)
	require.NoError(t, pool.pool.QueryRow(ctx, `
		SELECT data_type FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name = 'review_job_cloud_launches'
		  AND column_name = 'created_at'`).Scan(&dataTypeAgain))
	assert.Equal(t, "timestamp with time zone", dataTypeAgain)

	var whole, fraction string
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT TIMESTAMPTZ '1999-06-15 17:00:00+00'::text`).Scan(&whole))
	require.NoError(t, pool.pool.QueryRow(ctx, `SELECT TIMESTAMPTZ '1999-06-15 17:00:00.344331+00'::text`).Scan(&fraction))
	assert.Equal(t, "1999-06-15 17:00:00+00", whole)
	assert.Equal(t, "1999-06-15 17:00:00.344331+00", fraction)
	assert.Regexp(t, pgTimestamptzText, text)

	sqliteDB := openTestDB(t)
	t.Cleanup(func() { sqliteDB.Close() })
	var normalized sql.NullString
	require.NoError(t, sqliteDB.QueryRow(`SELECT datetime(?)`, text).Scan(&normalized))
	assert.False(t, normalized.Valid, "sqlite datetime accepted postgres text %q as %q", text, normalized.String)
}
