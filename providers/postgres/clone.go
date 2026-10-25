package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"regexp"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dotjoshrc/verity/internal/core"
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
func clone(ctx context.Context, db *sql.DB, source, name string, limit int) error {
	_, err := db.ExecContext(ctx, "CREATE DATABASE "+quote(name)+" TEMPLATE "+quote(source)+" OWNER "+quote(name)+" ALLOW_CONNECTIONS false CONNECTION LIMIT "+strconv.Itoa(limit))
	return err
}

func literal(s string) string {
	return "E'" + strings.NewReplacer(`\`, `\\`, "'", "''").Replace(s) + "'"
}

func restrictConnections(ctx context.Context, admin *sql.DB) error {
	// One transaction serializes ACL changes across providers and test processes.
	_, err := admin.ExecContext(ctx, `DO $$
 DECLARE db record;
 BEGIN
  PERFORM pg_advisory_xact_lock(774642019);
  FOR db IN
   SELECT datname FROM pg_database WHERE EXISTS (
    SELECT FROM aclexplode(COALESCE(datacl, acldefault('d', datdba)))
    WHERE grantee = 0 AND privilege_type = 'CONNECT'
   )
  LOOP
   BEGIN
    EXECUTE format('REVOKE CONNECT ON DATABASE %I FROM PUBLIC', db.datname);
   EXCEPTION WHEN invalid_catalog_name THEN
    NULL; -- Another suite may have released this database.
   END;
  END LOOP;
 END $$`)
	return err
}

func grantClone(ctx context.Context, admin *sql.DB, ep core.Endpoint, name string) error {
	if err := restrictConnections(ctx, admin); err != nil {
		return err
	}
	if _, err := admin.ExecContext(ctx, "ALTER DATABASE "+quote(name)+" ALLOW_CONNECTIONS true"); err != nil {
		return err
	}
	db, err := sql.Open("pgx", dsn(ep, name))
	if err != nil {
		return err
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT nspname FROM pg_namespace WHERE nspname !~ '^pg_' AND nspname <> 'information_schema'")
	if err != nil {
		return err
	}
	var schemas []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			rows.Close()
			return err
		}
		schemas = append(schemas, schema)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, schema := range schemas {
		// Cloning retains source object owners; grant access without changing the source.
		for _, statement := range []string{
			"GRANT USAGE, CREATE ON SCHEMA " + quote(schema) + " TO " + quote(name),
			"GRANT ALL ON ALL TABLES IN SCHEMA " + quote(schema) + " TO " + quote(name),
			"GRANT ALL ON ALL SEQUENCES IN SCHEMA " + quote(schema) + " TO " + quote(name),
			"GRANT ALL ON ALL ROUTINES IN SCHEMA " + quote(schema) + " TO " + quote(name),
		} {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return err
			}
		}
	}
	return nil
}
