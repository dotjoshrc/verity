package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	"github.com/moby/moby/client"
	"github.com/urfave/cli/v3"
)

func newDownCommand(afterDashDash []string) *cli.Command {
	return &cli.Command{
		Name:      "down",
		Usage:     "List or remove Verity Compose projects (including their volumes)",
		ArgsUsage: "[project]",
		Flags:     []cli.Flag{&cli.BoolFlag{Name: "all", Usage: "Remove every verity-* Compose project on the current Docker daemon"}},
		// main separates -- for go test; down treats its tail as ordinary operands.
		Action: func(ctx context.Context, c *cli.Command) error {
			return downAction(ctx, c, append(c.Args().Slice(), afterDashDash...))
		},
	}
}

func downAction(ctx context.Context, c *cli.Command, args []string) error {
	target := ""
	if len(args) > 0 {
		target = args[0]
	}
	if len(args) > 1 || (c.Bool("all") && len(args) > 0) {
		return errors.New("use `verity down [project]` or `verity down --all`")
	}
	if len(args) > 0 && !strings.HasPrefix(target, "verity-") {
		return fmt.Errorf("refusing to remove %q: project name must start with verity-", target)
	}
	dockerCLI, err := command.NewDockerCli()
	if err != nil {
		return fmt.Errorf("create docker cli: %w", err)
	}
	if err := dockerCLI.Initialize(&flags.ClientOptions{}); err != nil {
		return fmt.Errorf("initialize docker cli: %w", err)
	}
	service, err := compose.NewComposeService(dockerCLI)
	if err != nil {
		return fmt.Errorf("create compose service: %w", err)
	}
	projects, err := verityProjects(ctx, dockerCLI.Client())
	if err != nil {
		return err
	}
	if target == "" && !c.Bool("all") {
		for _, project := range projects {
			fmt.Fprintln(c.Writer, project)
		}
		if len(projects) == 0 {
			fmt.Fprintln(c.Writer, "No Verity Compose projects found.")
		}
		return errors.New("nothing removed: name a project or use `verity down --all`")
	}
	if target != "" {
		if !slices.Contains(projects, target) {
			return fmt.Errorf("Verity Compose project %q not found", target)
		}
		projects = []string{target}
	}
	var failures []error
	for _, project := range projects {
		downCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
		err := service.Down(downCtx, project, api.DownOptions{RemoveOrphans: true, Images: "local", Volumes: true})
		cancel()
		if err != nil {
			failures = append(failures, fmt.Errorf("remove %s: %w", project, err))
			continue
		}
		fmt.Fprintf(c.Writer, "Removed %s\n", project)
	}
	return errors.Join(failures...)
}

// Include resource-only projects left by an interrupted startup or teardown.
func verityProjects(ctx context.Context, docker client.APIClient) ([]string, error) {
	filter := make(client.Filters).Add("label", api.ProjectLabel)
	containers, err := docker.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("list Compose containers: %w", err)
	}
	networks, err := docker.NetworkList(ctx, client.NetworkListOptions{Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("list Compose networks: %w", err)
	}
	volumes, err := docker.VolumeList(ctx, client.VolumeListOptions{Filters: filter})
	if err != nil {
		return nil, fmt.Errorf("list Compose volumes: %w", err)
	}
	projects := map[string]bool{}
	add := func(labels map[string]string) {
		if project := labels[api.ProjectLabel]; strings.HasPrefix(project, "verity-") {
			projects[project] = true
		}
	}
	for _, c := range containers.Items {
		add(c.Labels)
	}
	for _, n := range networks.Items {
		add(n.Labels)
	}
	for _, v := range volumes.Items {
		add(v.Labels)
	}
	return slices.Sorted(maps.Keys(projects)), nil
}
