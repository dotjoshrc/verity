package main

import (
	"fmt"
	"slices"
	"strings"

	"github.com/urfave/cli/v3"
)

type Configuration struct {
	ComposeFile string
	GoTestArgs  []string
}

// newConfiguration builds the go test command line: the packages given before `--`
// (or go.dir when there are none), the flags built from config, then goArgs, the
// args after `--`, unchanged.
func newConfiguration(c *cli.Command, goArgs []string) *Configuration {
	packages := c.Args().Slice()
	if len(packages) == 0 {
		packages = []string{c.String("go-dir")}
	}

	cfg := &Configuration{ComposeFile: c.String("docker-compose-file"), GoTestArgs: append([]string{"test"}, packages...)}

	cfg.GoTestArgs = append(cfg.GoTestArgs,
		"-count=1", // Always disable go test cache
		fmt.Sprintf("-p=%d", c.Int("go-parallel-packages")),
		fmt.Sprintf("-parallel=%d", c.Int("go-parallel-tests")),
	)

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

	if run := c.String("go-run"); run != "" {
		cfg.GoTestArgs = append(cfg.GoTestArgs, "-run="+run)
	}

	if timeout := c.String("go-timeout"); timeout != "" {
		cfg.GoTestArgs = append(cfg.GoTestArgs, "-timeout="+timeout)
	}

	cfg.GoTestArgs = append(cfg.GoTestArgs, goArgs...)

	return cfg
}

// splitDashDash splits args at the first "--": before it are verity's flags and
// packages, after it go test's args. It runs before cli, which drops the "--" and
// with it the boundary.
func splitDashDash(args []string) (before, after []string) {
	if i := slices.Index(args, "--"); i >= 0 {
		return args[:i], args[i+1:]
	}

	return args, nil
}
