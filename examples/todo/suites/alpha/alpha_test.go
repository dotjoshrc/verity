package alpha

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/examples/todo/todotest"
	"github.com/dotjoshrc/verity/providers/postgres"
	"github.com/dotjoshrc/verity/providers/redis"
	"github.com/dotjoshrc/verity/providers/wiremock"
)

type suite struct {
	verity.Suite

	db    *postgres.Database
	cache *redis.Keyspace
	mock  *wiremock.Server
	base  string
}

func TestAlpha(t *testing.T) {
	todotest.SkipWithoutVerity(t)

	service, baseURL := todotest.Service()
	s := verity.NewSuite[suite](t, verity.Use("db"), verity.Use("cache"), verity.Use("mock"), verity.WithService(service))
	s.db = verity.Get[*postgres.Database](s, "db")
	s.cache = verity.Get[*redis.Keyspace](s, "cache")
	s.mock = verity.Get[*wiremock.Server](s, "mock")
	s.base = baseURL()

	verity.RunSuite(s)
}

// Both suites write to a table called todos. Each only ever sees its own rows,
// because each has its own clone of the database.
func (s *suite) TestTodosAreIsolated(t *testing.T) {
	title := todotest.CachedTitle(t, s.cache, s.mock, "alpha one")
	todotest.Post(t, s.base, title)
	todotest.Post(t, s.base, "alpha two")

	require.Equal(t, []string{"alpha one", "alpha two"}, todotest.List(t, s.base))

	var n int
	require.NoError(t, s.db.DB().QueryRow("SELECT count(*) FROM todos").Scan(&n))
	require.Equal(t, 2, n)
	if os.Getenv("TODO_FAIL") != "" {
		_, err := s.db.DB().ExecContext(t.Context(), "DO $$ BEGIN RAISE LOG 'todo failure-log marker'; END $$")
		require.NoError(t, err)
		t.Fatal("deliberate example failure")
	}
}
