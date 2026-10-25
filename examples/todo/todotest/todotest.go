// Package todotest holds what the example's suites share: starting the service
// under verity and talking to it.
package todotest

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/examples/todo"
	"github.com/dotjoshrc/verity/providers/redis"
	"github.com/dotjoshrc/verity/providers/wiremock"
	"github.com/stretchr/testify/require"
)

// SkipWithoutVerity skips the test under a plain `go test`, so the repo's own
// `go test ./...` passes; run these through `verity run` (see README.md).
func SkipWithoutVerity(t *testing.T) {
	t.Helper()

	if os.Getenv("VERITY_DISCOVERY") == "" {
		t.Skip("VERITY_DISCOVERY not set; run with `verity run` from examples/todo")
	}
}

// Service returns the verity.WithService function and a func that gives the
// service's base URL. Call the latter after NewSuite, which returns once the
// service is up.
func Service() (verity.ServiceFunc, func() string) {
	var base atomic.Pointer[string]

	fn := func(ctx context.Context, env verity.Env) error {
		// The listener is bound on all interfaces, so its address reads "[::]:port".
		_, port, _ := net.SplitHostPort(env.Listener().Addr().String())
		url := "http://127.0.0.1:" + port
		base.Store(&url)
		return todo.Run(ctx, env.Getenv, env.Listener())
	}

	return fn, func() string { return *base.Load() }
}

// Post creates a todo.
func Post(t *testing.T, base, title string) {
	t.Helper()

	resp, err := http.Post(base+"/todos", "application/json", strings.NewReader(`{"title":`+quote(title)+`}`))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("POST /todos: status %d", resp.StatusCode)
	}
}

// List returns every todo title the service has.
func List(t *testing.T, base string) []string {
	t.Helper()

	resp, err := http.Get(base + "/todos")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var titles []string
	if err := json.NewDecoder(resp.Body).Decode(&titles); err != nil {
		t.Fatal(err)
	}
	return titles
}

func quote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// CachedTitle fetches a suite-specific mock response and stores it in Redis.
// Both packages use the same path and key suffix to exercise provider isolation.
func CachedTitle(t *testing.T, cache *redis.Keyspace, mock *wiremock.Server, title string) string {
	t.Helper()
	mapping, err := json.Marshal(map[string]any{
		"request":  map[string]any{"method": "GET", "urlPath": "/title"},
		"response": map[string]any{"status": 200, "body": title},
	})
	require.NoError(t, err)
	require.NoError(t, mock.Register(t.Context(), mapping))
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, mock.URL()+"/title", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, title, string(body))
	key := cache.Prefix() + "title"
	require.NoError(t, cache.Client().Set(t.Context(), key, string(body), 0).Err())
	cached, err := cache.Client().Get(t.Context(), key).Result()
	require.NoError(t, err)
	require.Equal(t, title, cached)
	return cached
}
