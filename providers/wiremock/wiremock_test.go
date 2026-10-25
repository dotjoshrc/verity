package wiremock

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/dotjoshrc/verity/internal/core"
	"github.com/stretchr/testify/require"
)

func testEndpoint(t *testing.T, raw string) core.Endpoint {
	t.Helper()
	u, err := url.Parse(raw)
	require.NoError(t, err)
	port, err := strconv.Atoi(u.Port())
	require.NoError(t, err)
	return core.Endpoint{Name: "mock", Type: "wiremock", Host: u.Hostname(), Port: port}
}

func testResource(t *testing.T, endpoint core.Endpoint, opts ...any) *core.Resource {
	t.Helper()
	res, err := (&provider{}).Provision(t.Context(), core.Request{RunID: "run", SuiteID: t.Name(), Endpoint: endpoint, Options: opts})
	require.NoError(t, err)
	return res
}

func TestMapping(t *testing.T) {
	s := &Server{token: "suite"}
	for _, selector := range []string{"url", "urlPath", "urlPathTemplate"} {
		t.Run(selector, func(t *testing.T) {
			path := "/payments"
			if selector == "url" {
				path += "?page=1"
			}
			if selector == "urlPathTemplate" {
				path += "/{id}"
			}
			raw := json.RawMessage(`{"id":"caller-id","uuid":"alias","request":{"method":"POST","` + selector + `":"` + path + `","bodyPatterns":[{"equalToJson":"{}"}]},"response":{"status":201,"transformers":["my-extension"],"transformerParameters":{"large":9007199254740993}},"metadata":{"other":"keep","veritySuite":"foreign"},"scenarioName":"payment","requiredScenarioState":"Started","newScenarioState":"done","custom":{"keep":true}}`)
			body, err := s.mapping(raw)
			require.NoError(t, err)
			var got map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(body, &got))
			var request map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(got["request"], &request))
			require.JSONEq(t, `"/verity/suite`+path+`"`, string(request[selector]))
			require.JSONEq(t, `[{"equalToJson":"{}"}]`, string(request["bodyPatterns"]))
			require.JSONEq(t, `{"status":201,"transformers":["my-extension"],"transformerParameters":{"large":9007199254740993}}`, string(got["response"]))
			require.JSONEq(t, `{"other":"keep","veritySuite":"suite"}`, string(got["metadata"]))
			require.JSONEq(t, `"suite:payment"`, string(got["scenarioName"]))
			require.JSONEq(t, `"Started"`, string(got["requiredScenarioState"]))
			require.JSONEq(t, `"done"`, string(got["newScenarioState"]))
			require.JSONEq(t, `{"keep":true}`, string(got["custom"]))
			require.Regexp(t, `^"[0-9a-f-]{36}"$`, string(got["id"]))
			require.NotContains(t, got, "uuid")
			again, err := s.mapping(raw)
			require.NoError(t, err)
			require.NotEqual(t, body, again, "each registration gets a fresh ID")
			require.Contains(t, string(raw), `"id":"caller-id"`, "caller input is unchanged")
		})
	}
}

func TestRejectInvalidMappings(t *testing.T) {
	s := &Server{token: "suite"}
	for name, raw := range map[string]string{
		"invalid JSON": `{`, "null": `null`, "array": `[]`, "no request": `{}`, "null request": `{"request":null}`,
		"no selector":       `{"request":{"method":"ANY"}}`,
		"two selectors":     `{"request":{"url":"/x","urlPath":"/x"}}`,
		"regex":             `{"request":{"urlPattern":".*"}}`,
		"path regex":        `{"request":{"urlPathPattern":"/.*"}}`,
		"non-string":        `{"request":{"urlPath":4}}`,
		"null path":         `{"request":{"urlPath":null}}`,
		"absolute":          `{"request":{"url":"http://elsewhere/x"}}`,
		"relative":          `{"request":{"url":"x"}}`,
		"authority":         `{"request":{"url":"//elsewhere/x"}}`,
		"fragment":          `{"request":{"url":"/x#fragment"}}`,
		"dot segments":      `{"request":{"url":"/../x"}}`,
		"encoded traversal": `{"request":{"url":"/%2e%2e/x"}}`,
		"bad escape":        `{"request":{"url":"/%xx"}}`,
		"path query":        `{"request":{"urlPath":"/x?q=1"}}`,
		"bad metadata":      `{"request":{"url":"/x"},"metadata":null}`,
		"bad scenario":      `{"request":{"url":"/x"},"scenarioName":5}`,
		"null scenario":     `{"request":{"url":"/x"},"scenarioName":null}`,
	} {
		t.Run(name, func(t *testing.T) { _, err := s.mapping(json.RawMessage(raw)); require.Error(t, err) })
	}
}

func TestRegisterAndRelease(t *testing.T) {
	var requests []string
	var bodies []string
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/json", r.Header.Get("Content-Type"))
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		requests = append(requests, r.URL.Path)
		bodies = append(bodies, string(body))
		w.WriteHeader(http.StatusCreated)
	}))
	defer admin.Close()
	res := testResource(t, testEndpoint(t, admin.URL), EnvName("PAYMENTS_URL"))
	s := res.Handle.(*Server)
	require.Equal(t, map[string]string{"PAYMENTS_URL": s.URL()}, res.Env)
	require.NoError(t, s.Register(t.Context(), json.RawMessage(`{"request":{"urlPath":"/payments"},"response":{"status":200}}`)))
	require.NoError(t, res.Release(t.Context()))
	require.NoError(t, res.Release(t.Context()), "cleanup can be retried")
	require.ErrorContains(t, s.Register(t.Context(), json.RawMessage(`{}`)), "released")
	require.Equal(t, []string{"/__admin/mappings", "/__admin/mappings/remove-by-metadata", "/__admin/mappings/remove-by-metadata"}, requests)
	require.JSONEq(t, `{"matchesJsonPath":{"expression":"$.veritySuite","equalTo":"`+s.token+`"}}`, bodies[1])
	require.Equal(t, bodies[1], bodies[2])
	other := testResource(t, testEndpoint(t, admin.URL))
	require.NotEqual(t, s.URL(), other.Handle.(*Server).URL())
	require.Contains(t, other.Env, "MOCK_BASE_URL")
}

func TestAdminErrors(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "invalid mapping", http.StatusUnprocessableEntity)
	}))
	s := testResource(t, testEndpoint(t, admin.URL)).Handle.(*Server)
	mapping := json.RawMessage(`{"request":{"url":"/x"}}`)
	require.ErrorContains(t, s.Register(t.Context(), mapping), "HTTP 422: invalid mapping")
	require.ErrorContains(t, s.release(t.Context()), "HTTP 422")
	admin.Close()
	require.Error(t, testResource(t, testEndpoint(t, admin.URL)).Handle.(*Server).Register(t.Context(), mapping))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, s.post(ctx, "/__admin/mappings", []byte(`{}`)), context.Canceled)
}

func TestProvisionRejectsBadRequests(t *testing.T) {
	good := core.Request{RunID: "r", SuiteID: "s", Endpoint: core.Endpoint{Host: "127.0.0.1", Port: 8080}}
	for _, opts := range [][]any{{42}, {EnvName("")}, {EnvName("BAD=NAME")}, {EnvName("BAD\x00NAME")}} {
		req := good
		req.Options = opts
		_, err := (&provider{}).Provision(t.Context(), req)
		require.Error(t, err)
	}
	for _, field := range []string{"run", "suite", "host", "port"} {
		req := good
		switch field {
		case "run":
			req.RunID = ""
		case "suite":
			req.SuiteID = ""
		case "host":
			req.Endpoint.Host = ""
		case "port":
			req.Endpoint.Port = 0
		}
		_, err := (&provider{}).Provision(t.Context(), req)
		require.Error(t, err)
	}
}

func TestAdminRedirectIsNotFollowed(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer admin.Close()
	s := testResource(t, testEndpoint(t, admin.URL)).Handle.(*Server)
	require.ErrorContains(t, s.Register(t.Context(), json.RawMessage(`{"request":{"url":"/x"}}`)), "HTTP 307")
}

func TestAdminErrorIsBounded(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, strings.Repeat("x", 10000), http.StatusBadRequest)
	}))
	defer admin.Close()
	s := testResource(t, testEndpoint(t, admin.URL)).Handle.(*Server)
	err := s.Register(t.Context(), json.RawMessage(`{"request":{"url":"/x"}}`))
	require.Error(t, err)
	require.Less(t, len(err.Error()), 4200)
}
