package main

import (
	"fmt"
	"strings"

	"github.com/urfave/cli/v3"
)

type Configuration struct {
	ComposeFile string
	GoTestArgs  []string
}

func newConfiguration(c *cli.Command) *Configuration {
	dir := c.String("go-dir")

	cfg := &Configuration{
		ComposeFile: c.String("docker-compose-file"),
		GoTestArgs: []string{
			"test",
			dir,
			"-count=1", // Always disable go test cache
			fmt.Sprintf("-p=%d", c.Int("go-parallel-packages")),
			fmt.Sprintf("-parallel=%d", c.Int("go-parallel-tests")),
		},
	}

	if c.Bool("go-fail-fast") {
		cfg.GoTestArgs = append(cfg.GoTestArgs, "-failfast")
	}

	tags := c.StringSlice("go-tags")
	if len(tags) > 0 {
		cfg.GoTestArgs = append(cfg.GoTestArgs, fmt.Sprintf(`-tags=%s`, strings.Join(tags, ",")))
	}

	if c.Bool("go-verbose") {
		cfg.GoTestArgs = append(cfg.GoTestArgs, "-v")
	}

	return cfg
}
