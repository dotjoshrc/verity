// Package wiremock provisions suite-scoped HTTP stubs on a shared WireMock 3 server.
// Applications must preserve the path prefix in MOCK_BASE_URL. This prevents
// accidental collisions, not access to other suites or the shared admin API.
package wiremock

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"uuid"

	"github.com/dotjoshrc/verity/internal/core"
)

func init() { core.RegisterProvider("wiremock", &provider{}) }

type envName string

// EnvName sets the env var carrying the suite's base URL. The default is MOCK_BASE_URL.
func EnvName(name string) any { return envName(name) }

// Server is one suite's namespace on a shared WireMock server.
// Do not use it after the suite releases its resources.
type Server struct {
	base, token string
	client      *http.Client
	mu          sync.Mutex
	closed      bool
}

// URL returns the suite's base URL, including its path prefix and no trailing slash.
func (s *Server) URL() string { return s.base + "/verity/" + s.token }

// Register adds a native WireMock mapping. Exactly one of url, urlPath, or
// urlPathTemplate must select a path starting with /. Regex selectors are not
// supported. Mapping IDs, scenario names, and veritySuite metadata are managed
// by the provider. Response and extension fields are passed through unchanged.
func (s *Server) Register(ctx context.Context, mapping json.RawMessage) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("wiremock: server is released")
	}
	body, err := s.mapping(mapping)
	if err != nil {
		return err
	}
	return s.post(ctx, "/__admin/mappings", body)
}

type provider struct{}

func (*provider) Provision(_ context.Context, req core.Request) (*core.Resource, error) {
	name := "MOCK_BASE_URL"
	for _, opt := range req.Options {
		switch opt := opt.(type) {
		case envName:
			name = string(opt)
		default:
			return nil, fmt.Errorf("wiremock: unknown option type %T", opt)
		}
	}
	if name == "" || strings.ContainsAny(name, "=\x00") {
		return nil, errors.New("wiremock: EnvName must be a valid nonempty environment variable name")
	}
	if req.RunID == "" || req.SuiteID == "" {
		return nil, errors.New("wiremock: run ID and suite ID must be nonempty")
	}
	if req.Endpoint.Host == "" || req.Endpoint.Port < 1 || req.Endpoint.Port > 65535 {
		return nil, errors.New("wiremock: endpoint needs a host and port between 1 and 65535")
	}
	s := &Server{
		base:   (&url.URL{Scheme: "http", Host: net.JoinHostPort(req.Endpoint.Host, strconv.Itoa(req.Endpoint.Port))}).String(),
		token:  rand.Text(),
		client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}
	return &core.Resource{Env: map[string]string{name: s.URL()}, Handle: s, Release: s.release}, nil
}

func (s *Server) release(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	body, err := json.Marshal(map[string]any{"matchesJsonPath": map[string]string{"expression": "$.veritySuite", "equalTo": s.token}})
	if err != nil {
		return err
	}
	return s.post(ctx, "/__admin/mappings/remove-by-metadata", body)
}

func object(raw json.RawMessage, field string) (map[string]json.RawMessage, error) {
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil || obj == nil {
		return nil, fmt.Errorf("wiremock: %s must be a JSON object", field)
	}
	return obj, nil
}

func (s *Server) mapping(raw json.RawMessage) ([]byte, error) {
	mapping, err := object(raw, "mapping")
	if err != nil {
		return nil, err
	}
	request, err := object(mapping["request"], "request")
	if err != nil {
		return nil, err
	}
	var selector string
	for _, key := range []string{"url", "urlPath", "urlPathTemplate", "urlPattern", "urlPathPattern"} {
		if _, ok := request[key]; !ok {
			continue
		}
		if selector != "" {
			return nil, errors.New("wiremock: request needs exactly one URL selector")
		}
		selector = key
	}
	if selector == "" {
		return nil, errors.New("wiremock: request needs exactly one URL selector")
	}
	// shortcut: regex URLs are unsupported; add safe prefix handling when needed.
	if selector == "urlPattern" || selector == "urlPathPattern" {
		return nil, errors.New("wiremock: regex URL selectors are not supported; use url, urlPath, or urlPathTemplate")
	}
	var path string
	if err := json.Unmarshal(request[selector], &path); err != nil {
		return nil, fmt.Errorf("wiremock: %s must be a path string", selector)
	}
	u, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.Contains(path, "#") {
		return nil, errors.New("wiremock: URL selector must be a relative path starting with / and without a fragment")
	}
	if selector != "url" && (u.RawQuery != "" || u.ForceQuery) {
		return nil, errors.New("wiremock: query strings require url or queryParameters")
	}
	for _, part := range strings.Split(u.Path, "/") {
		if part == "." || part == ".." {
			return nil, errors.New("wiremock: URL selector must not contain dot segments")
		}
	}
	request[selector], _ = json.Marshal("/verity/" + s.token + path)
	mapping["request"], err = json.Marshal(request)
	if err != nil {
		return nil, err
	}
	metadata := map[string]json.RawMessage{}
	if raw, ok := mapping["metadata"]; ok {
		metadata, err = object(raw, "metadata")
		if err != nil {
			return nil, err
		}
	}
	metadata["veritySuite"], _ = json.Marshal(s.token)
	mapping["metadata"], err = json.Marshal(metadata)
	if err != nil {
		return nil, err
	}
	if raw, ok := mapping["scenarioName"]; ok {
		var scenario string
		if err := json.Unmarshal(raw, &scenario); err != nil || scenario == "" {
			return nil, errors.New("wiremock: scenarioName must be a nonempty string")
		}
		mapping["scenarioName"], _ = json.Marshal(s.token + ":" + scenario)
	}
	delete(mapping, "uuid") // WireMock accepts uuid as an alias for id.
	mapping["id"], _ = json.Marshal(uuid.NewV7().String())
	return json.Marshal(mapping)
}

func (s *Server) post(ctx context.Context, path string, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.base+path, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("wiremock: %s: %w", path, err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("wiremock: %s: %w", path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		detail, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("wiremock: %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return err
}
