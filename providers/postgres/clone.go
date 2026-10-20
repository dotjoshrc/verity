package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

// adminDatabase is the maintenance database verity connects to for CREATE
// DATABASE. It must differ from the source: Postgres refuses to clone a database
// with sessions, and verity's own admin connection would be one.
const adminDatabase = "postgres"

var unsafeChars = regexp.MustCompile(`[^a-z0-9_]`)

func clean(s string, max int) string {
	s = unsafeChars.ReplaceAllString(strings.ToLower(s), "_")
	return s[:min(len(s), max)]
}

// databaseName returns a database name for one suite. Postgres silently truncates
// identifiers at 63 bytes, so the parts are bounded here (7+20+1+32 = 60). The
// suite ID is hashed rather than cut, so IDs that share a long prefix, or differ
// only in characters the name cannot hold, still get distinct names.
func databaseName(runID, suiteID string) (string, error) {
	if runID == "" || suiteID == "" {
		return "", errors.New("postgres: request needs a run ID and a suite ID")
	}

	sum := sha256.Sum256([]byte(suiteID))

	return "verity_" + clean(runID, 20) + "_" + hex.EncodeToString(sum[:16]), nil
}

func quote(name string) string { return pgx.Identifier{name}.Sanitize() }

// clone creates name as a copy of source. Postgres itself waits (about 5s) for
// sessions still connected to source to exit, so no retry is needed here; if one
// outlasts that, the error says the source is being accessed by other users.
func clone(ctx context.Context, db *sql.DB, source, name string) error {
	_, err := db.ExecContext(ctx, "CREATE DATABASE "+quote(name)+" TEMPLATE "+quote(source))
	return err
}
