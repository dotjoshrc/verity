package main

import (
	"context"
	"testing"
	"time"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/stretchr/testify/require"
)

// fakeCompose answers Ps with a fixed list; the embedded nil interface panics
// if awaitReady ever calls anything else.
type fakeCompose struct {
	api.Compose
	containers []api.ContainerSummary
}

func (f fakeCompose) Ps(context.Context, string, api.PsOptions) ([]api.ContainerSummary, error) {
	return f.containers, nil
}

func (f fakeCompose) Logs(context.Context, string, api.LogConsumer, api.LogOptions) error { return nil }

func TestAwaitReady(t *testing.T) {
	project := &types.Project{Name: "p", Services: types.Services{
		"db":     {Name: "db"},
		"schema": {Name: "schema", Labels: types.Labels{"verity.wait": "1"}},
		"other":  {Name: "other"},
	}}
	c := func(service, state, health string, exit int) api.ContainerSummary {
		return api.ContainerSummary{Service: service, State: container.ContainerState(state), Health: container.HealthStatus(health), ExitCode: exit}
	}

	tests := []struct {
		name       string
		containers []api.ContainerSummary
		wantErr    string
	}{
		{"healthy db, schema done", []api.ContainerSummary{c("db", "running", "healthy", 0), c("schema", "exited", "", 0)}, ""},
		{"running without healthcheck is ready", []api.ContainerSummary{c("db", "running", "", 0)}, ""},
		{"schema failed", []api.ContainerSummary{c("db", "running", "healthy", 0), c("schema", "exited", "", 1)}, `service "schema" failed: exited (exit 1)`},
		{"db unhealthy", []api.ContainerSummary{c("db", "running", "unhealthy", 0)}, `service "db" failed: unhealthy (exit 0)`},
		{"unlabelled service exited 0", []api.ContainerSummary{c("other", "exited", "", 0)}, `service "other" failed: exited (exit 0)`},
		{"db still starting", []api.ContainerSummary{c("db", "running", "starting", 0)}, "waiting for db: context deadline exceeded"},
		{"schema still running", []api.ContainerSummary{c("schema", "running", "", 0)}, "waiting for schema: context deadline exceeded"},
		{"db not started", []api.ContainerSummary{c("db", "created", "", 0)}, "waiting for db: context deadline exceeded"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := awaitReady(context.Background(), fakeCompose{containers: tt.containers}, project, 50*time.Millisecond)
			if tt.wantErr == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.wantErr)
			}
		})
	}
}
