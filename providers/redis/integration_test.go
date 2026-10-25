package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"strconv"
	"sync"
	"testing"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/internal/core"
)

// endpoint reads the fixture Redis from VERITY_TEST_REDIS (a redis:// URL with
// the admin password) and skips when it is unset.
func endpoint(t *testing.T) core.Endpoint {
	t.Helper()

	raw := os.Getenv("VERITY_TEST_REDIS")
	if raw == "" {
		t.Skip("VERITY_TEST_REDIS not set; see the test-integration mise task")
	}

	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	pw, _ := u.User.Password()

	return core.Endpoint{Name: "cache", Type: "redis", Host: u.Hostname(), Port: port, User: u.User.Username(), Password: pw}
}

func randHex(t *testing.T) string {
	t.Helper()

	b := make([]byte, 8)
	_, err := rand.Read(b)
	require.NoError(t, err)

	return hex.EncodeToString(b)
}

// provision provisions one suite and releases it when the test ends.
func provision(t *testing.T, p *provider, ep core.Endpoint, runID string, opts ...any) (*Keyspace, map[string]string) {
	t.Helper()

	res, err := p.Provision(context.Background(), core.Request{RunID: runID, SuiteID: randHex(t), Endpoint: ep, Options: opts})
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Release(context.Background()) })

	return res.Handle.(*Keyspace), res.Env
}

func TestProvisionConcurrent(t *testing.T) {
	ep := endpoint(t)

	// Two providers stand in for two test processes, each with its own admin client.
	providers := []*provider{{}, {}}
	runID := randHex(t)[:6]

	const n = 8
	suiteIDs := make([]string, n) // generated here: require must not run in a goroutine
	for i := range n {
		suiteIDs[i] = randHex(t)
	}
	spaces := make([]*Keyspace, n)
	envs := make([]map[string]string, n)
	releases := make([]func(context.Context) error, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			res, err := providers[i%2].Provision(context.Background(), core.Request{RunID: runID, SuiteID: suiteIDs[i], Endpoint: ep})
			if err != nil {
				errs[i] = err
				return
			}
			spaces[i], envs[i], releases[i] = res.Handle.(*Keyspace), res.Env, res.Release
		})
	}
	wg.Wait()

	for _, r := range releases {
		if r != nil { // release whatever was created, even if an assertion below fails
			t.Cleanup(func() { _ = r(context.Background()) })
		}
	}

	ctx := context.Background()
	prefixes := map[string]bool{}
	users := map[string]bool{}
	for i := range n {
		require.NoError(t, errs[i])
		prefixes[spaces[i].Prefix()] = true
		users[spaces[i].user] = true
		require.Equal(t, map[string]string{"REDIS_URL": spaces[i].URL(), "REDIS_KEY_PREFIX": spaces[i].Prefix()}, envs[i])

		require.NoError(t, spaces[i].Client().Set(ctx, spaces[i].Prefix()+"k", "mine", 0).Err())
	}
	require.Len(t, prefixes, n, "distinct prefixes")
	require.Len(t, users, n, "distinct users")

	// A suite reads its own key and nobody else's.
	a, b := spaces[0], spaces[1]
	got, err := a.Client().Get(ctx, a.Prefix()+"k").Result()
	require.NoError(t, err)
	require.Equal(t, "mine", got)

	foreign := b.Prefix() + "k"
	require.ErrorContains(t, a.Client().Get(ctx, foreign).Err(), "NOPERM")
	require.ErrorContains(t, a.Client().Set(ctx, foreign, "x", 0).Err(), "NOPERM")
	require.ErrorContains(t, a.Client().Del(ctx, foreign).Err(), "NOPERM")
	require.ErrorContains(t, a.Client().Publish(ctx, b.Prefix()+"ch", "x").Err(), "NOPERM")
	require.NoError(t, a.Client().Publish(ctx, a.Prefix()+"ch", "x").Err())

	// The commands that reach across suites or the server are refused.
	for _, cmd := range [][]any{
		{"FLUSHALL"},
		{"FLUSHDB"},
		{"KEYS", "*"},
		{"CONFIG", "GET", "maxmemory"},
		{"SELECT", 1},
		{"RANDOMKEY"},
		{"DBSIZE"},
		{"INFO"},
		{"SCRIPT", "FLUSH"},
		{"FUNCTION", "LOAD", "x"},
		{"PUBSUB", "CHANNELS"},
		{"ACL", "LIST"},
		{"CLIENT", "LIST"},
	} {
		require.ErrorContains(t, a.Client().Do(ctx, cmd...).Err(), "NOPERM", "%v", cmd)
	}
}

func TestEnvNames(t *testing.T) {
	_, env := provision(t, &provider{}, endpoint(t), "r", EnvName("CACHE_URL"), PrefixEnvName("CACHE_PREFIX"))

	require.Contains(t, env, "CACHE_URL")
	require.Contains(t, env, "CACHE_PREFIX")
	require.Len(t, env, 2)
}

func TestReleaseRemovesUserAndKeys(t *testing.T) {
	ep := endpoint(t)
	p := &provider{}
	ctx := context.Background()

	res, err := p.Provision(ctx, core.Request{RunID: "r", SuiteID: randHex(t), Endpoint: ep})
	require.NoError(t, err)
	k := res.Handle.(*Keyspace)

	// Enough keys to need more than one UNLINK batch.
	pipe := k.Client().Pipeline()
	for i := range 1200 {
		pipe.Set(ctx, k.Prefix()+strconv.Itoa(i), "v", 0)
	}
	_, err = pipe.Exec(ctx)
	require.NoError(t, err)

	admin := p.admin(ep)
	require.NoError(t, res.Release(ctx))
	require.NoError(t, res.Release(ctx), "safe to repeat")

	require.ErrorIs(t, admin.Do(ctx, "ACL", "GETUSER", k.user).Err(), goredis.Nil, "user is gone")
	keys, _, err := admin.Scan(ctx, 0, k.Prefix()+"*", 10000).Result()
	require.NoError(t, err)
	require.Empty(t, keys, "keys are gone")
	require.Error(t, k.Client().Ping(ctx).Err(), "the client is closed")
}

// A forgotten verity.password label (no password) and a wrong one are the likely
// misconfigurations; both must name the label.
func TestBadAdminPasswordNamesTheLabel(t *testing.T) {
	ep := endpoint(t)
	if ep.Password == "" {
		t.Skip("the fixture has no password, so any password is accepted")
	}

	for name, password := range map[string]string{"missing": "", "wrong": "not-the-password"} {
		t.Run(name, func(t *testing.T) {
			bad := ep
			bad.Password = password

			_, err := (&provider{}).Provision(context.Background(), core.Request{RunID: "r", SuiteID: randHex(t), Endpoint: bad})
			require.ErrorContains(t, err, "verity.password")
		})
	}
}

type cacheSuite struct{ verity.Suite }

// TestThroughNewSuite runs the real provider through verity.NewSuite: it checks
// the init registration, verity.Get, and that t.Cleanup releases the resource.
func TestThroughNewSuite(t *testing.T) {
	ep := endpoint(t)

	doc, err := core.Discovery{RunID: randHex(t)[:6], Backends: map[string]core.Endpoint{"cache": ep}}.Encode()
	require.NoError(t, err)
	t.Setenv(core.DiscoveryEnv, doc)

	var k *Keyspace
	t.Run("suite", func(t *testing.T) {
		s := verity.NewSuite[cacheSuite](t, verity.Use("cache"))
		k = verity.Get[*Keyspace](s, "cache")

		require.NoError(t, k.Client().Set(context.Background(), k.Prefix()+"k", "v", 0).Err())
	})

	require.NotNil(t, k)
	require.Error(t, k.Client().Ping(context.Background()).Err(), "the suite's cleanup released the user")
}
