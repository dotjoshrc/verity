package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/internal/core"
)

// endpoint reads the fixture Postgres from VERITY_TEST_POSTGRES (a DSN) and
// skips when it is unset.
func endpoint(t *testing.T) core.Endpoint {
	t.Helper()

	raw := os.Getenv("VERITY_TEST_POSTGRES")
	if raw == "" {
		t.Skip("VERITY_TEST_POSTGRES not set; see the test-integration mise task")
	}

	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	pw, _ := u.User.Password()

	require.Greater(t, len(u.Path), 1, "VERITY_TEST_POSTGRES needs a database path")

	return core.Endpoint{Name: "db", Type: "postgres", Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw, Database: u.Path[1:]}
}

func randHex(t *testing.T) string {
	t.Helper()

	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)

	return hex.EncodeToString(b)
}

func open(t *testing.T, ep core.Endpoint, database string) *sql.DB {
	t.Helper()

	db, err := sql.Open("pgx", dsn(ep, database))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })

	return db
}

func dropOnCleanup(t *testing.T, admin *sql.DB, names ...string) {
	t.Helper()

	t.Cleanup(func() {
		for _, n := range names {
			_, _ = admin.Exec("DROP DATABASE IF EXISTS " + quote(n) + " WITH (FORCE)")
		}
	})
}

// sourceDatabase creates a database with a schema and a seed row, standing in
// for what the compose stack's schema step leaves behind. It returns the endpoint
// pointing at it, with every connection to it closed so it can be cloned.
func sourceDatabase(t *testing.T, ep core.Endpoint) core.Endpoint {
	t.Helper()

	admin := open(t, ep, adminDatabase)
	name := "verity_src_'$$\\" + randHex(t)
	_, err := admin.Exec("CREATE DATABASE " + quote(name))
	require.NoError(t, err)
	dropOnCleanup(t, admin, name)

	src, err := sql.Open("pgx", dsn(ep, name))
	require.NoError(t, err)
	_, err = src.Exec("CREATE TABLE items (id serial PRIMARY KEY, v text); INSERT INTO items (v) VALUES ('seed'); CREATE SCHEMA extra; CREATE TABLE extra.items (id serial PRIMARY KEY, v text);")
	require.NoError(t, err)
	require.NoError(t, src.Close())

	ep.Database = name

	return ep
}

func TestProvisionConcurrent(t *testing.T) {
	ep := sourceDatabase(t, endpoint(t))

	// Two providers stand in for two test processes, each with its own admin pool.
	providers := []*provider{{}, {}}
	runID := randHex(t)[:6]

	const n = 8
	suiteIDs := make([]string, n) // generated here: require must not run in a goroutine
	for i := range n {
		suiteIDs[i] = randHex(t)
	}
	dbs := make([]*Database, n)
	envs := make([]map[string]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			res, err := providers[i%2].Provision(context.Background(), core.Request{RunID: runID, SuiteID: suiteIDs[i], Endpoint: ep})
			if err != nil {
				errs[i] = err
				return
			}
			dbs[i], envs[i] = res.Handle.(*Database), res.Env
		})
	}
	wg.Wait()

	for _, s := range dbs {
		if s != nil { // drop whatever was created, even if an assertion below fails
			t.Cleanup(func() { require.NoError(t, s.release(context.Background())) })
		}
	}

	names := map[string]bool{}
	for i := range n {
		require.NoError(t, errs[i])
		names[dbs[i].Name()] = true
		require.Equal(t, map[string]string{"DATABASE_URL": dbs[i].DSN()}, envs[i])

		var count int
		require.NoError(t, dbs[i].DB().QueryRow("SELECT count(*) FROM items").Scan(&count))
		require.Equal(t, 1, count, "seed row is cloned")
	}
	require.Len(t, names, n, "distinct databases")

	// A write in one database is invisible in the others.
	_, err := dbs[0].DB().Exec("INSERT INTO items (v) VALUES ('mine')")
	require.NoError(t, err)
	for _, s := range dbs[1:] {
		var count int
		require.NoError(t, s.DB().QueryRow("SELECT count(*) FROM items").Scan(&count))
		require.Equal(t, 1, count)
	}

	// Release closes the pool and is safe to repeat.
	for _, s := range dbs {
		require.NoError(t, s.release(context.Background()))
		require.NoError(t, s.release(context.Background()))
	}
}

func TestEnvName(t *testing.T) {
	ep := sourceDatabase(t, endpoint(t))

	res, err := (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: randHex(t), Endpoint: ep, Options: []any{EnvName("PG_URL")}})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, res.Release(context.Background())) })

	require.Equal(t, map[string]string{"PG_URL": res.Handle.(*Database).DSN()}, res.Env)
}

func TestLingeringSessionOnSourceDoesNotFailTheClone(t *testing.T) {
	ep := sourceDatabase(t, endpoint(t))

	// Postgres waits for sessions on the source to exit, and we rely on that
	// instead of retrying. sql.Open does not connect, so hold a real session.
	pool := open(t, ep, ep.Database)
	held, err := pool.Conn(context.Background())
	require.NoError(t, err)
	time.AfterFunc(300*time.Millisecond, func() {
		_ = held.Close() // returns the conn to the pool; closing the pool ends the session
		_ = pool.Close()
	})

	res, err := (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: randHex(t), Endpoint: ep})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, res.Release(context.Background())) })
}

func TestMissingSourceIsAnError(t *testing.T) {
	ep := endpoint(t)
	ep.Database = "verity_does_not_exist"

	suiteID := randHex(t)
	_, err := (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: suiteID, Endpoint: ep})
	require.ErrorContains(t, err, "verity_does_not_exist")
	name, err := databaseName("r", suiteID)
	require.NoError(t, err)
	var count int
	require.NoError(t, open(t, ep, adminDatabase).QueryRow("SELECT count(*) FROM pg_roles WHERE rolname = $1", name).Scan(&count))
	require.Zero(t, count, "failed provisioning removes its role")
}

type dbSuite struct{ verity.Suite }

// TestThroughNewSuite checks real provisioning and cleanup for parallel subtests.
func TestThroughNewSuite(t *testing.T) {
	ep := sourceDatabase(t, endpoint(t))
	admin := open(t, ep, adminDatabase)
	doc, err := core.Discovery{RunID: randHex(t)[:6], Backends: map[string]core.Endpoint{"db": ep}}.Encode()
	require.NoError(t, err)
	t.Setenv(core.DiscoveryEnv, doc)

	dbs := make([]*Database, 2)
	t.Run("cases", func(t *testing.T) {
		for i := range dbs {
			t.Run(strconv.Itoa(i), func(t *testing.T) {
				t.Parallel()
				s := verity.NewSuite[dbSuite](t, verity.Use("db"))
				db := verity.Get[*Database](s, "db")
				dbs[i] = db

				var count int
				require.NoError(t, db.DB().QueryRow("SELECT count(*) FROM items").Scan(&count))
				require.Equal(t, 1, count, "each subtest starts with only the seed")
				_, err := db.DB().Exec("INSERT INTO items (id, v) VALUES (2, $1)", t.Name())
				require.NoError(t, err, "the same primary key can be used in both subtests")
				var value string
				require.NoError(t, db.DB().QueryRow("SELECT v FROM items WHERE id = 2").Scan(&value))
				require.Equal(t, t.Name(), value)
			})
		}
	})

	require.NotNil(t, dbs[0])
	require.NotNil(t, dbs[1])
	require.NotEqual(t, dbs[0].Name(), dbs[1].Name())
	for _, db := range dbs {
		require.Error(t, db.DB().Ping(), "the subtest's cleanup released its pool")
		var exists bool
		require.NoError(t, admin.QueryRow("SELECT EXISTS (SELECT FROM pg_database WHERE datname = $1) OR EXISTS (SELECT FROM pg_roles WHERE rolname = $1)", db.Name()).Scan(&exists))
		require.False(t, exists, "the subtest's cleanup removed its database and role")
	}
}

func TestRoleIsolationAndConnectionLimit(t *testing.T) {
	ep := sourceDatabase(t, endpoint(t))
	admin := open(t, ep, adminDatabase)
	p := &provider{}
	provision := func(options ...any) *Database {
		res, err := p.Provision(context.Background(), core.Request{RunID: randHex(t), SuiteID: randHex(t), Endpoint: ep, Options: options})
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, res.Release(context.Background())) })
		return res.Handle.(*Database)
	}
	a, b := provision(ConnLimit(2)), provision()
	cfg, err := url.Parse(a.DSN())
	require.NoError(t, err)
	require.NotEqual(t, ep.User, cfg.User.Username())
	for _, target := range []string{b.Name(), ep.Database, "postgres", "template1"} {
		cfg.Path = "/" + target
		db, err := sql.Open("pgx", cfg.String())
		require.NoError(t, err)
		err = db.Ping()
		require.ErrorContains(t, err, "permission denied for database")
		require.NoError(t, db.Close())
	}
	for _, statement := range []string{"CREATE ROLE forbidden", "CREATE DATABASE forbidden", "ALTER SYSTEM SET max_connections = '200'"} {
		_, err := a.DB().Exec(statement)
		require.Error(t, err)
	}
	_, err = a.DB().Exec("INSERT INTO items(v) VALUES ('suite'); INSERT INTO extra.items(v) VALUES ('suite'); CREATE TABLE own_table (id int)")
	require.NoError(t, err, "cloned tables, sequences, and schema are usable")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c1, err := a.DB().Conn(ctx)
	require.NoError(t, err)
	defer c1.Close()
	c2, err := a.DB().Conn(ctx)
	require.NoError(t, err)
	defer c2.Close()
	_, err = a.DB().Conn(ctx)
	require.ErrorContains(t, err, "too many connections for database")
	// The same database limit does not cap the administrative superuser.
	super := open(t, ep, a.Name())
	for range 5 {
		conn, err := super.Conn(ctx)
		require.NoError(t, err)
		defer conn.Close()
	}
	require.NoError(t, c1.Close())
	require.NoError(t, c2.Close())
	require.NoError(t, a.release(ctx))
	require.NoError(t, a.release(ctx))
	var count int
	require.NoError(t, admin.QueryRow("SELECT count(*) FROM pg_roles WHERE rolname = $1", a.Name()).Scan(&count))
	require.Zero(t, count)
	require.NoError(t, admin.QueryRow("SELECT count(*) FROM pg_database WHERE datname = $1", a.Name()).Scan(&count))
	require.Zero(t, count)
}

func TestProvisionRejectsNonSuperuserAdmin(t *testing.T) {
	ep := sourceDatabase(t, endpoint(t))
	admin := open(t, ep, adminDatabase)
	role := "verity_admin_" + randHex(t)
	password := randHex(t)
	_, err := admin.Exec("CREATE ROLE " + quote(role) + " LOGIN CREATEDB CREATEROLE PASSWORD " + literal(password))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := admin.Exec("REVOKE CONNECT ON DATABASE postgres FROM " + quote(role) + "; DROP ROLE " + quote(role))
		require.NoError(t, err)
	})
	_, err = admin.Exec("GRANT CONNECT ON DATABASE postgres TO " + quote(role))
	require.NoError(t, err)
	ep.User, ep.Password = role, password
	_, err = (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: randHex(t), Endpoint: ep})
	require.ErrorContains(t, err, "must be a superuser")
}
