package wiremock

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"testing"

	"github.com/dotjoshrc/verity"
	"github.com/dotjoshrc/verity/internal/core"
	"github.com/stretchr/testify/require"
)

func integrationEndpoint(t *testing.T) core.Endpoint {
	t.Helper()
	raw := os.Getenv("VERITY_TEST_WIREMOCK")
	if raw == "" {
		t.Skip("set VERITY_TEST_WIREMOCK to the fixture URL; see README")
	}
	return testEndpoint(t, raw)
}

func getStub(t *testing.T, address string, status int, body string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, address, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	got, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, status, resp.StatusCode, string(got))
	if status == http.StatusOK {
		require.Equal(t, body, string(got))
	}
}

func TestSharedServerIsolation(t *testing.T) {
	ep := integrationEndpoint(t)
	a, b := testResource(t, ep), testResource(t, ep)
	t.Cleanup(func() {
		require.NoError(t, a.Release(context.Background()))
		require.NoError(t, b.Release(context.Background()))
	})
	servers := []*Server{a.Handle.(*Server), b.Handle.(*Server)}
	t.Run("parallel suites", func(t *testing.T) {
		for i, s := range servers {
			t.Run(fmt.Sprint(i), func(t *testing.T) {
				t.Parallel()
				// Identical scenario names and paths must not share state across suites.
				first := fmt.Sprintf(`{"scenarioName":"flow","requiredScenarioState":"Started","newScenarioState":"done","request":{"method":"GET","urlPath":"/same"},"response":{"status":200,"body":"suite-%d"}}`, i)
				second := fmt.Sprintf(`{"scenarioName":"flow","requiredScenarioState":"done","request":{"method":"GET","urlPath":"/same"},"response":{"status":200,"body":"done-%d"}}`, i)
				require.NoError(t, s.Register(t.Context(), json.RawMessage(first)))
				require.NoError(t, s.Register(t.Context(), json.RawMessage(second)))
				getStub(t, s.URL()+"/same", 200, fmt.Sprintf("suite-%d", i))
				getStub(t, s.URL()+"/same", 200, fmt.Sprintf("done-%d", i))
			})
		}
	})
	require.NoError(t, a.Release(t.Context()))
	getStub(t, servers[0].URL()+"/same", 404, "")
	getStub(t, servers[1].URL()+"/same", 200, "done-1")
	require.NoError(t, b.Release(t.Context()))
	getStub(t, servers[1].URL()+"/same", 404, "")
}

type wiremockSuite struct{ verity.Suite }

func TestThroughNewSuite(t *testing.T) {
	ep := integrationEndpoint(t)
	doc, err := (core.Discovery{RunID: "wiremock-integration", Backends: map[string]core.Endpoint{"mock": ep}}).Encode()
	require.NoError(t, err)
	t.Setenv(core.DiscoveryEnv, doc)
	var server *Server
	t.Run("suite", func(t *testing.T) {
		suite := verity.NewSuite[wiremockSuite](t, verity.Use("mock"))
		server = verity.Get[*Server](suite, "mock")
		require.NoError(t, server.Register(t.Context(), json.RawMessage(`{"request":{"method":"GET","urlPathTemplate":"/customers/{id}"},"response":{"status":200,"jsonBody":{"name":"Ada"}}}`)))
		getStub(t, server.URL()+"/customers/42", 200, `{"name":"Ada"}`)
	})
	require.NotNil(t, server)
	getStub(t, server.URL()+"/customers/42", 404, "")
	require.ErrorContains(t, server.Register(t.Context(), json.RawMessage(`{}`)), "released")
}
