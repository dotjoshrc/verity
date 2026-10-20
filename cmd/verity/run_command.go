package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/charmbracelet/log"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/docker/compose/v5/pkg/compose"
	altsrc "github.com/urfave/cli-altsrc/v3"
	yaml "github.com/urfave/cli-altsrc/v3/yaml"
	"github.com/urfave/cli/v3"
)

func sourceChain(key string) cli.ValueSourceChain {
	configFiles := []string{"./.verity.yaml", "./.verity.yml", "./verity.yaml", "./verity.yml"}
	var sources []cli.ValueSource
	for _, file := range configFiles {
		sources = append(sources, yaml.YAML(key, altsrc.StringSourcer(file)))
	}
	sources = append(sources, cli.EnvVar("VERITY_"+strings.ReplaceAll(strings.ToUpper(key), ".", "_")))
	return cli.NewValueSourceChain(sources...)
}

func newRunCommand() *cli.Command {
	return &cli.Command{
		Name: "run",
		Flags: []cli.Flag{
			&cli.StringFlag{
				Name:    "go-dir",
				Usage:   "Directory to run tests in. This should be a go package. Default is ./...",
				Value:   "./...",
				Sources: sourceChain("go.dir"),
			},
			&cli.IntFlag{
				Name:    "go-parallel-packages",
				Usage:   "Number of go packages to run in parallel.",
				Value:   2,
				Sources: sourceChain("go.parallel_packages"),
			},
			&cli.IntFlag{
				Name:    "go-parallel-tests",
				Usage:   "Max number of go tests to run in parallel in a package.",
				Value:   1,
				Sources: sourceChain("go.parallel_tests"),
			},
			&cli.StringSliceFlag{
				Name:    "go-tags",
				Usage:   "A list of build tags during testing.",
				Sources: sourceChain("go.tags"),
			},
			&cli.BoolFlag{
				Name:    "go-verbose",
				Usage:   "Verbose output.",
				Sources: sourceChain("go.verbose"),
			},
			&cli.BoolFlag{
				Name:    "go-fail-fast",
				Usage:   "Fail fast during tests. True by default.",
				Value:   true,
				Sources: sourceChain("go.fail_fast"),
			},
			&cli.StringFlag{
				Name:    "docker-compose-file",
				Usage:   "Path to the docker compose file.",
				Value:   "./docker-compose.yaml",
				Sources: sourceChain("docker_compose.file"),
			},
		},
		Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
			if c.Bool("debug") {
				log.SetLevel(log.DebugLevel)
			}

			configuration := newConfiguration(c)
			c.Metadata["configuration"] = configuration
			log.Debugf("configuration set: %+v", configuration)

			dockerCLI, err := command.NewDockerCli()
			if err != nil {
				return nil, fmt.Errorf("create docker cli: %w", err)
			}
			err = dockerCLI.Initialize(&flags.ClientOptions{})
			if err != nil {
				return ctx, fmt.Errorf("initialize docker cli: %w", err)
			}

			service, err := compose.NewComposeService(dockerCLI)
			if err != nil {
				return ctx, fmt.Errorf("create compose service: %w", err)
			}

			project, err := service.LoadProject(ctx, api.ProjectLoadOptions{
				ConfigPaths: []string{configuration.ComposeFile},
				ProjectName: "verity",
			})
			if err != nil {
				return ctx, fmt.Errorf("load compose project: %w", err)
			}

			err = service.Up(ctx, project, api.UpOptions{
				Create: api.CreateOptions{
					Build: &api.BuildOptions{},
				},
				Start: api.StartOptions{
					Wait: true,
				},
			})
			if err != nil {
				downErr := service.Down(ctx, "verity", api.DownOptions{
					RemoveOrphans: true,
					Images:        "local",
				})
				if downErr != nil {
					return ctx, fmt.Errorf("start compose services: %w; additionally, failed to clean up compose services: %v", err, downErr)
				}

				return ctx, fmt.Errorf("start compose services: %w", err)
			}

			c.Metadata["docker_compose_service"] = service

			return ctx, nil
		},
		After: func(ctx context.Context, c *cli.Command) error {
			// Before can fail before compose is up; nothing to tear down then.
			service, ok := c.Metadata["docker_compose_service"].(api.Compose)
			if !ok {
				return nil
			}

			err := service.Down(ctx, "verity", api.DownOptions{
				RemoveOrphans: true,
				Images:        "local",
			})
			if err != nil {
				return fmt.Errorf("stop compose services: %w", err)
			}

			return nil
		},
		Action: func(ctx context.Context, c *cli.Command) error {
			return newTestCommand(c).Run()
		},
	}
}
