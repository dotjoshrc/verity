package main

import (
	"context"
	"testing"

	"github.com/compose-spec/compose-go/v2/loader"
	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/dotjoshrc/verity/internal/core"
	"github.com/stretchr/testify/require"
)

func loadFixture(t *testing.T, yaml string) *types.Project {
	t.Helper()

	project, err := loader.LoadWithContext(context.Background(), types.ConfigDetails{
		WorkingDir:  t.TempDir(),
		ConfigFiles: []types.ConfigFile{{Filename: "compose.yaml", Content: []byte(yaml)}},
	}, func(o *loader.Options) { o.SetProjectName("verity-test", true) })
	require.NoError(t, err)

	return project
}

func TestBuildDiscovery(t *testing.T) {
	tests := []struct {
		name    string
		compose string
		want    map[string]core.Endpoint
		wantErr string
	}{
		{
			name: "defaults from env map form",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
    environment: {POSTGRES_PASSWORD: secret}
`,
			want: map[string]core.Endpoint{
				"db": {Name: "db", Type: "postgres", Port: 5432, User: "postgres", Password: "secret", Database: "postgres"},
			},
		},
		{
			name: "redis: default user, password from the label only",
			compose: `
services:
  cache:
    image: redis
    labels: {verity.type: redis, verity.password: admin}
    environment: {POSTGRES_PASSWORD: ignored}
  open:
    image: redis
    labels: {verity.type: redis, verity.port: "6380"}
`,
			want: map[string]core.Endpoint{
				"cache": {Name: "cache", Type: "redis", Port: 6379, User: "default", Password: "admin"},
				"open":  {Name: "open", Type: "redis", Port: 6380, User: "default"},
			},
		},
		{
			name: "env list form; database defaults to user",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
    environment: [POSTGRES_USER=app, POSTGRES_PASSWORD=pw]
`,
			want: map[string]core.Endpoint{
				"db": {Name: "db", Type: "postgres", Port: 5432, User: "app", Password: "pw", Database: "app"},
			},
		},
		{
			name: "nil env value is unset",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
    environment: [POSTGRES_USER]
`,
			want: map[string]core.Endpoint{
				"db": {Name: "db", Type: "postgres", Port: 5432, User: "postgres", Database: "postgres"},
			},
		},
		{
			name: "labels override env and defaults",
			compose: `
services:
  db:
    image: postgres
    labels:
      verity.type: postgres
      verity.port: "6543"
      verity.user: admin
      verity.password: adminpw
      verity.database: main
    environment: {POSTGRES_USER: ignored, POSTGRES_PASSWORD: ignored, POSTGRES_DB: ignored}
`,
			want: map[string]core.Endpoint{
				"db": {Name: "db", Type: "postgres", Port: 6543, User: "admin", Password: "adminpw", Database: "main"},
			},
		},
		{
			name: "services without verity.type are ignored",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
  cache:
    image: redis
  other:
    image: busybox
    labels: {foo: bar}
`,
			want: map[string]core.Endpoint{
				"db": {Name: "db", Type: "postgres", Port: 5432, User: "postgres", Database: "postgres"},
			},
		},
		{
			name: "unknown type",
			compose: `
services:
  cache:
    image: memcached
    labels: {verity.type: memcached}
`,
			wantErr: `service "cache": unknown verity.type "memcached"`,
		},
		{
			name: "network_mode that cannot publish ports",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
    network_mode: host
`,
			wantErr: `service "db": network_mode "host" cannot publish a port for verity`,
		},
		{
			name: "bad port",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres, verity.port: abc}
`,
			wantErr: `service "db": verity.port "abc" is not a port number`,
		},
		{
			name: "_FILE var without a label is an error",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
    environment: {POSTGRES_PASSWORD_FILE: /run/secrets/pw}
`,
			wantErr: `service "db": POSTGRES_PASSWORD_FILE is not supported; set the verity.password label instead`,
		},
		{
			name: "_FILE var is fine when the label covers it",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres, verity.password: pw}
    environment: {POSTGRES_PASSWORD_FILE: /run/secrets/pw}
`,
			want: map[string]core.Endpoint{
				"db": {Name: "db", Type: "postgres", Port: 5432, User: "postgres", Password: "pw", Database: "postgres"},
			},
		},
		{
			name: "port out of range",
			compose: `
services:
  db:
    image: postgres
    labels: {verity.type: postgres, verity.port: "70000"}
`,
			wantErr: `service "db": verity.port "70000" is not a port number`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, err := buildDiscovery(loadFixture(t, tt.compose), "run1")
			if tt.wantErr != "" {
				require.EqualError(t, err, tt.wantErr)
				return
			}

			require.NoError(t, err)
			require.Equal(t, "run1", d.RunID)
			require.Equal(t, tt.want, d.Backends)

			// The encoded document must round-trip through the test-side decoder.
			s, err := d.Encode()
			require.NoError(t, err)
			got, err := core.DecodeDiscovery(s)
			require.NoError(t, err)
			require.Equal(t, d, got)
		})
	}
}

func TestPublishAndFill(t *testing.T) {
	project := loadFixture(t, `
services:
  db:
    image: postgres
    labels: {verity.type: postgres}
    ports: ["5432:5432"]
  other:
    image: busybox
    ports: ["8080"]
`)
	d, err := buildDiscovery(project, "run1")
	require.NoError(t, err)

	publishLoopback(project, d)
	require.Equal(t, []types.ServicePortConfig{{HostIP: "127.0.0.1", Target: 5432, Protocol: "tcp"}}, project.Services["db"].Ports)
	require.Len(t, project.Services["other"].Ports, 1) // unlabelled services are untouched

	require.EqualError(t, fillAddresses(d, nil), `service "db": no published port for 5432`)

	require.NoError(t, fillAddresses(d, []api.ContainerSummary{{Service: "db", Publishers: api.PortPublishers{
		{TargetPort: 5432, PublishedPort: 0}, // an unpublished entry is skipped
		{TargetPort: 5432, PublishedPort: 32769, URL: "127.0.0.1"},
	}}}))
	require.Equal(t, "127.0.0.1", d.Backends["db"].Host)
	require.Equal(t, 32769, d.Backends["db"].Port)
}

func TestRequireHealthchecks(t *testing.T) {
	check := func(extra string) error {
		project := loadFixture(t, "services:\n  db:\n    image: postgres\n    labels: {verity.type: postgres}\n"+extra)
		d, err := buildDiscovery(project, "run1")
		require.NoError(t, err)
		return requireHealthchecks(project, d)
	}

	require.NoError(t, check("    healthcheck: {test: [CMD, pg_isready]}\n"))
	want := `service "db": labelled services need a healthcheck in the compose file`
	require.EqualError(t, check(""), want)
	require.EqualError(t, check("    healthcheck: {disable: true}\n"), want)
	require.EqualError(t, check("    healthcheck: {test: [NONE]}\n"), want)
}

func TestDiscoveryOptions(t *testing.T) {
	project := loadFixture(t, `
services:
  db:
    image: postgres
    labels:
      verity.type: postgres
      verity.opt.setting: value
      verity.unknown: ignored
`)
	d, err := buildDiscovery(project, "run1")
	require.NoError(t, err)
	require.Equal(t, project.Name, d.Project)
	require.Equal(t, map[string]string{"setting": "value"}, d.Backends["db"].Extra)
}
