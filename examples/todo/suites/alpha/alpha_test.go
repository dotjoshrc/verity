package alpha

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/examples/todo/todotest"
	"github.com/dotjoshrc/verity/providers/postgres"
)

type suite struct {
	verity.Suite

	db   *postgres.Database
	base string
}

func TestAlpha(t *testing.T) {
	todotest.SkipWithoutVerity(t)

	service, baseURL := todotest.Service()
	s := verity.NewSuite[suite](t, verity.Use("db"), verity.WithService(service))
	s.db = verity.Get[*postgres.Database](s, "db")
	s.base = baseURL()

	verity.RunSuite(s)
}

// Both suites write to a table called todos. Each only ever sees its own rows,
// because each has its own clone of the database.
func (s *suite) TestTodosAreIsolated(t *testing.T) {
	todotest.Post(t, s.base, "alpha one")
	todotest.Post(t, s.base, "alpha two")

	require.Equal(t, []string{"alpha one", "alpha two"}, todotest.List(t, s.base))

	var n int
	require.NoError(t, s.db.DB().QueryRow("SELECT count(*) FROM todos").Scan(&n))
	require.Equal(t, 2, n)
}
