// Copyright 2026 nexus-gateway contributors
// SPDX-License-Identifier: Apache-2.0

package storeforward

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func readUserVersion(t *testing.T, db *sql.DB) int {
	t.Helper()
	var v int
	require.NoError(t, db.QueryRow("PRAGMA user_version").Scan(&v))
	return v
}

// A fresh database is stamped at the current schema version (#29).
func TestMigrate_FreshStampsCurrentVersion(t *testing.T) {
	buf, err := Open(t.TempDir()+"/sf.db", 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })
	assert.Equal(t, schemaVersion, readUserVersion(t, buf.db))
}

// A pre-stamp (version 0) database with existing frames migrates in place,
// keeps its frames, and is stamped to the current version (#29).
func TestMigrate_V0MigratesInPlaceKeepingFrames(t *testing.T) {
	path := t.TempDir() + "/sf.db"
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec(`
		CREATE TABLE frames (
			seq        INTEGER PRIMARY KEY AUTOINCREMENT,
			gateway_id TEXT NOT NULL DEFAULT '',
			point_id   TEXT NOT NULL,
			value      REAL NOT NULL,
			timestamp  TEXT NOT NULL
		);
		CREATE TABLE cursor (id INTEGER PRIMARY KEY CHECK (id = 1), seq INTEGER NOT NULL DEFAULT 0);
		INSERT INTO frames (gateway_id, point_id, value, timestamp)
			VALUES ('gw-1', 'p-1', 1.5, '2026-01-01T00:00:00Z');
	`)
	require.NoError(t, err)
	// user_version left at its default of 0 (a pre-stamp database).
	require.NoError(t, raw.Close())

	buf, err := Open(path, 100)
	require.NoError(t, err)
	t.Cleanup(func() { buf.Close() })

	assert.Equal(t, schemaVersion, readUserVersion(t, buf.db))
	assert.Equal(t, int64(1), buf.Depth(), "existing frame must survive the migration")
}

// The stamped schema version survives a full close/reopen (persisted to the file,
// not just held on the migrating connection) (#29).
func TestMigrate_VersionPersistsAcrossReopen(t *testing.T) {
	path := t.TempDir() + "/sf.db"
	buf, err := Open(path, 100)
	require.NoError(t, err)
	require.NoError(t, buf.Close())

	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { raw.Close() })
	assert.Equal(t, schemaVersion, readUserVersion(t, raw), "user_version must persist to disk")
}

// A database stamped newer than this binary understands aborts Open (fail-fast,
// safe-downgrade), leaving the file untouched (#29).
func TestMigrate_TooNewAborts(t *testing.T) {
	path := t.TempDir() + "/sf.db"
	raw, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = raw.Exec("PRAGMA user_version = 999")
	require.NoError(t, err)
	require.NoError(t, raw.Close())

	_, err = Open(path, 100)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "newer")
}
