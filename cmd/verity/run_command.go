package main

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/log"
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
				Name:    "go-run",
				Usage:   "Only run tests matching this regexp (go test -run).",
				Sources: sourceChain("go.run"),
			},
			&cli.StringFlag{
				Name:    "go-timeout",
				Usage:   "Test timeout (go test -timeout). Go's own default is 10m.",
				Sources: sourceChain("go.timeout"),
			},
			&cli.DurationFlag{
				Name:    "docker-compose-wait-timeout",
				Usage:   "How long to wait for services to become ready.",
				Value:   2 * time.Minute,
				Sources: sourceChain("docker_compose.wait_timeout"),
			},
			&cli.BoolFlag{
				Name:  "keep",
				Usage: "Leave the compose stack up and print how to reach it.",
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

			return ctx, nil
		},
		Action: runAction,
	}
}
