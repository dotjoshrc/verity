package redis

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity/internal/core"
)

func TestNames(t *testing.T) {
	user, prefix, err := names("Run-1", "suite-a")
	require.NoError(t, err)
	require.Regexp(t, `^verity_[a-z0-9_]+$`, user)
	require.Regexp(t, `^verity:[a-z0-9_]+:$`, prefix, "no glob characters, so it is safe in an ACL pattern")
	require.Contains(t, user, "run_1")

	again, againPrefix, err := names("Run-1", "suite-a")
	require.NoError(t, err)
	require.Equal(t, user, again, "stable")
	require.Equal(t, prefix, againPrefix)

	// IDs that share a long prefix, or differ only in characters the names cannot
	// hold, must not collide; neither may the same suite in another run.
	long := strings.Repeat("x", 100)
	seen := map[string]bool{user: true}
	for _, ids := range [][2]string{{"Run-1", long + "1"}, {"Run-1", long + "2"}, {"Run-1", "suite_a"}, {"Run-1", "Suite-A"}, {"run_1", "suite-a"}, {"Run-2", "suite-a"}} {
		got, gotPrefix, err := names(ids[0], ids[1])
		require.NoError(t, err)
		require.False(t, seen[got], "%v collides", ids)
		seen[got] = true
		require.NotContains(t, gotPrefix, "*")
	}

	_, _, err = names(strings.Repeat("r", 100), "s")
	require.NoError(t, err)

	_, _, err = names("", "s")
	require.Error(t, err)
	_, _, err = names("r", "")
	require.Error(t, err)
}

func TestACLSetUser(t *testing.T) {
	args := aclSetUser("verity_u", "pw", "verity:r:")

	got := make([]string, len(args))
	for i, a := range args {
		got[i] = a.(string)
	}
	require.Equal(t, []string{"ACL", "SETUSER", "verity_u"}, got[:3])
	require.Contains(t, got, ">pw")
	require.Contains(t, got, "~verity:r:*")
	require.Contains(t, got, "&verity:r:*")
	require.Contains(t, got, "-@dangerous")
	for _, deny := range []string{"-select", "-randomkey", "-dbsize", "-script|flush", "-function", "-pubsub"} {
		require.Contains(t, got, deny)
	}
	require.Less(t, indexOf(got, "reset"), indexOf(got, "~verity:r:*"), "reset must come before the rules it would erase")
	require.Less(t, indexOf(got, "resetchannels"), indexOf(got, "&verity:r:*"))
}

func indexOf(s []string, v string) int {
	for i := range s {
		if s[i] == v {
			return i
		}
	}

	return -1
}

func TestConnURL(t *testing.T) {
	ep := core.Endpoint{Host: "127.0.0.1", Port: 6380}

	require.Equal(t, "redis://u:pw@127.0.0.1:6380", connURL("u", "pw", ep))
	require.Equal(t, "redis://default:pw@127.0.0.1:6380", connURL("", "pw", ep))
	require.Equal(t, "redis://127.0.0.1:6380", connURL("default", "", ep), "no password, no auth")
}

func TestExplain(t *testing.T) {
	require.ErrorContains(t, explain(errors.New("NOAUTH Authentication required.")), "verity.password")
	require.ErrorContains(t, explain(errors.New("WRONGPASS invalid username-password pair or user is disabled.")), "verity.password")
	require.ErrorContains(t, explain(errors.New("ERR Protocol error: unauthenticated multibulk length")), "verity.password")
	require.ErrorContains(t, explain(errors.New("ERR unknown command `ACL`, with args beginning with: `SETUSER`")), "Redis 7")
	require.ErrorContains(t, explain(errors.New("ERR Error in ACL SETUSER modifier '-script|flush': Unknown command or category name in ACL")), "Redis 7")

	plain := errors.New("connection refused")
	require.Equal(t, plain, explain(plain))
}

func TestProvisionRejectsBadRequests(t *testing.T) {
	p := &provider{}

	_, err := p.Provision(context.Background(), core.Request{RunID: "r", SuiteID: "s", Options: []any{42}})
	require.ErrorContains(t, err, "unknown option type int")

	_, err = p.Provision(context.Background(), core.Request{SuiteID: "s"})
	require.ErrorContains(t, err, "run ID")
}
