package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity/internal/core"
)

func TestDatabaseName(t *testing.T) {
	name, err := databaseName("Run-1", "suite-a")
	require.NoError(t, err)
	require.LessOrEqual(t, len(name), 63)
	require.Regexp(t, `^[a-z0-9_]+$`, name)
	require.Contains(t, name, "run_1")

	again, err := databaseName("Run-1", "suite-a")
	require.NoError(t, err)
	require.Equal(t, name, again, "stable")

	// Suite IDs that share a long prefix, or differ only in characters a name
	// cannot hold, must not collide.
	long := strings.Repeat("x", 100)
	for _, other := range []string{long + "1", long + "2", "suite_a", "Suite-A"} {
		got, err := databaseName("Run-1", other)
		require.NoError(t, err)
		require.NotEqual(t, name, got)
		require.LessOrEqual(t, len(got), 63)
	}
	a, _ := databaseName("r", long+"1")
	b, _ := databaseName("r", long+"2")
	require.NotEqual(t, a, b)

	longRun, err := databaseName(strings.Repeat("r", 100), "s")
	require.NoError(t, err)
	require.LessOrEqual(t, len(longRun), 63)

	_, err = databaseName("", "s")
	require.Error(t, err)
	_, err = databaseName("r", "")
	require.Error(t, err)
}

func TestDSN(t *testing.T) {
	ep := core.Endpoint{Host: "db", Port: 5432, User: "us@er", Password: "p@ss/w:rd#?", Database: "postgres"}

	cfg, err := pgconn.ParseConfig(dsn(ep, "verity_x"))
	require.NoError(t, err)
	require.Equal(t, "us@er", cfg.User)
	require.Equal(t, "p@ss/w:rd#?", cfg.Password)
	require.Equal(t, "db", cfg.Host)
	require.EqualValues(t, 5432, cfg.Port)
	require.Equal(t, "verity_x", cfg.Database)
}

func TestProvisionRejectsUnknownOption(t *testing.T) {
	_, err := (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: "s", Options: []any{42}})
	require.ErrorContains(t, err, "unknown option type int")
}

func TestProvisionRejectsBadSource(t *testing.T) {
	for source, want := range map[string]string{
		"":         "no database to clone",
		"postgres": "own database",
	} {
		t.Run(source, func(t *testing.T) {
			_, err := (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: "s", Endpoint: core.Endpoint{Database: source}})
			require.ErrorContains(t, err, want)
		})
	}
}

func TestProvisionRejectsInvalidConnectionLimit(t *testing.T) {
	for _, limit := range []int{0, -1} {
		_, err := (&provider{}).Provision(context.Background(), core.Request{Options: []any{ConnLimit(limit)}})
		require.ErrorContains(t, err, "ConnLimit must be positive")
	}
}
