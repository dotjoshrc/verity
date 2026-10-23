// Package todotest holds what the example's suites share: starting the service
// under verity and talking to it.
package todotest

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/examples/todo"
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
