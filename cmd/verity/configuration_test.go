package main

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

func parseConfiguration(t *testing.T, args ...string) *Configuration {
	t.Helper()

	t.Chdir(t.TempDir()) // keep stray ./verity.yaml files out of the source chain

	for _, key := range []string{"DIR", "FAIL_FAST"} {
		t.Setenv("VERITY_GO_"+key, "") // registers restore of the original value
		require.NoError(t, os.Unsetenv("VERITY_GO_"+key))
	}

	var cfg *Configuration
	cmd := &cli.Command{
		Name:  "run",
		Flags: newRunCommand().Flags,
		Action: func(_ context.Context, c *cli.Command) error {
			cfg = newConfiguration(c)
			return nil
		},
	}
	require.NoError(t, cmd.Run(context.Background(), append([]string{"run"}, args...)))

	return cfg
}

func TestNewConfiguration(t *testing.T) {
	t.Run("reads go-dir", func(t *testing.T) {
		cfg := parseConfiguration(t, "--go-dir", "./examples/...")
		require.Contains(t, cfg.GoTestArgs, "./examples/...")
	})

	t.Run("fail fast defaults to true", func(t *testing.T) {
		require.Contains(t, parseConfiguration(t).GoTestArgs, "-failfast")
	})

	t.Run("args after -- pass through, after the flags", func(t *testing.T) {
		args := parseConfiguration(t, "--go-timeout", "5m", "--", "-run", "TestX").GoTestArgs
		require.Equal(t, []string{"-timeout=5m", "-run", "TestX"}, args[len(args)-3:])
	})

	t.Run("fail fast can be disabled", func(t *testing.T) {
		require.NotContains(t, parseConfiguration(t, "--go-fail-fast=false").GoTestArgs, "-failfast")
	})
}
