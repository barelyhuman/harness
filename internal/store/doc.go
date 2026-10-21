// Package store is the SQLite persistence layer (spec 02): modernc.org/sqlite
// in WAL mode at ~/Library/Application Support/Harness/harness.db, with
// embedded SQL migrations (migrations/NNN_name.sql) applied in order and
// tracked via PRAGMA user_version.
package store
