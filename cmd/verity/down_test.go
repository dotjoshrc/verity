package main

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/api/types/volume"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

type projectClient struct {
	client.APIClient
	fail string
}

func (c projectClient) ContainerList(_ context.Context, opts client.ContainerListOptions) (client.ContainerListResult, error) {
	if c.fail == "containers" {
		return client.ContainerListResult{}, errors.New("unavailable")
	}
	return client.ContainerListResult{Items: []container.Summary{
		{Labels: map[string]string{api.ProjectLabel: "verity-b"}},
		{Labels: map[string]string{api.ProjectLabel: "other"}},
		{Labels: map[string]string{}},
	}}, nil
}
func (c projectClient) NetworkList(context.Context, client.NetworkListOptions) (client.NetworkListResult, error) {
	if c.fail == "networks" {
		return client.NetworkListResult{}, errors.New("unavailable")
	}
	return client.NetworkListResult{Items: []network.Summary{{Labels: map[string]string{api.ProjectLabel: "verity-a"}}}}, nil
}
func (c projectClient) VolumeList(context.Context, client.VolumeListOptions) (client.VolumeListResult, error) {
	if c.fail == "volumes" {
		return client.VolumeListResult{}, errors.New("unavailable")
	}
	return client.VolumeListResult{Items: []volume.Volume{
		{Labels: map[string]string{api.ProjectLabel: "verity-b"}},
		{Labels: map[string]string{api.ProjectLabel: "verity-c"}},
	}}, nil
}

func TestVerityProjects(t *testing.T) {
	projects, err := verityProjects(context.Background(), projectClient{})
	require.NoError(t, err)
	require.Equal(t, []string{"verity-a", "verity-b", "verity-c"}, projects)
	for _, resource := range []string{"containers", "networks", "volumes"} {
		t.Run(resource, func(t *testing.T) {
			projects, err := verityProjects(context.Background(), projectClient{fail: resource})
			require.ErrorContains(t, err, "list Compose "+resource)
			require.Nil(t, projects) // Never act on a partial inventory.
		})
	}
}

func TestDownInvalidArguments(t *testing.T) {
	for _, args := range [][]string{{"other"}, {"--all", "verity-a"}, {"verity-a", "verity-b"}, {"--", "unrelated"}, {"--all", "--", "verity-a"}, {"--all", ""}, {""}} {
		t.Run(args[0], func(t *testing.T) {
			var out bytes.Buffer
			before, tail := splitDashDash(append([]string{"verity", "down"}, args...))
			app := newApp(tail)
			app.Writer, app.ErrWriter = &out, &out
			err := app.Run(context.Background(), before)
			require.Error(t, err)
			require.NotContains(t, err.Error(), "docker")
		})
	}
}
