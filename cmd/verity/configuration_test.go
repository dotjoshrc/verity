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

	before, goArgs := splitDashDash(args)

	var cfg *Configuration
	cmd := &cli.Command{
		Name:  "run",
		Flags: newRunCommand(nil).Flags,
		Action: func(_ context.Context, c *cli.Command) error {
			cfg = newConfiguration(c, goArgs)
			return nil
		},
	}
	require.NoError(t, cmd.Run(context.Background(), append([]string{"run"}, before...)))

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

	t.Run("go-dir is the default package", func(t *testing.T) {
		require.Equal(t, "./...", parseConfiguration(t).GoTestArgs[1])
		require.Equal(t, "./x/...", parseConfiguration(t, "--go-dir", "./x/...", "--", "-race", "-run", "TestX/sub").GoTestArgs[1])
	})

	t.Run("packages before -- replace go-dir", func(t *testing.T) {
		args := parseConfiguration(t, "--go-dir", "./x/...", "./pkg/a", "./pkg/b", "--", "-race").GoTestArgs
		require.Equal(t, []string{"test", "./pkg/a", "./pkg/b"}, args[:3])
		require.NotContains(t, args, "./x/...")
		require.Equal(t, "-race", args[len(args)-1])
	})

	t.Run("args after -- are never packages", func(t *testing.T) {
		args := parseConfiguration(t, "--go-dir", "./x/...", "--", "-coverprofile", "./c.out", "./data").GoTestArgs
		require.Equal(t, "./x/...", args[1])
		require.Equal(t, []string{"-coverprofile", "./c.out", "./data"}, args[len(args)-3:])
	})

	t.Run("fail fast can be disabled", func(t *testing.T) {
		require.NotContains(t, parseConfiguration(t, "--go-fail-fast=false").GoTestArgs, "-failfast")
	})
}

func TestSplitDashDash(t *testing.T) {
	before, after := splitDashDash([]string{"verity", "run", "./a", "--", "-run", "--", "x"})
	require.Equal(t, []string{"verity", "run", "./a"}, before)
	require.Equal(t, []string{"-run", "--", "x"}, after, "only the first -- splits")

	before, after = splitDashDash([]string{"verity", "run"})
	require.Equal(t, []string{"verity", "run"}, before)
	require.Empty(t, after)
}
