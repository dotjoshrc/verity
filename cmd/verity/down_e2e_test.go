package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/flags"
	"github.com/stretchr/testify/require"
)

// TestDownDocker requires an exclusive Docker daemon: it exercises --all.
func TestDownDocker(t *testing.T) {
	if os.Getenv("VERITY_TEST_DOCKER") == "" {
		t.Skip("set VERITY_TEST_DOCKER=1 to run against Docker")
	}
	dockerCLI, err := command.NewDockerCli()
	require.NoError(t, err)
	require.NoError(t, dockerCLI.Initialize(&flags.ClientOptions{}))
	inventory := func() []string {
		projects, err := verityProjects(context.Background(), dockerCLI.Client())
		require.NoError(t, err)
		return projects
	}
	require.Empty(t, inventory(), "use an exclusive daemon with no existing Verity projects")
	volumesBefore := dockerLines(t, "volume", "ls", "-q")
	bin := verityBuild(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := fixtureCommand(bin, []string{"FIXTURE_SLEEP=2m", "FIXTURE_REPORT_PGID=1"}, "--keep", "--", "-v", "-run", "TestSlow")
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	require.NoError(t, cmd.Start())
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	// Reap the CLI even if setup fails before the deliberate SIGKILL.
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	var group int
	scanner := bufio.NewScanner(stdout)
	for scanner.Scan() {
		if value, ok := strings.CutPrefix(scanner.Text(), "fixture process group: "); ok {
			group, err = strconv.Atoi(value)
			require.NoError(t, err)
			break
		}
	}
	require.NotZero(t, group, "fixture did not start: %s", stderr.String())
	t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
	projects := dockerLines(t, "ps", "--filter", "label=com.docker.compose.service=db", "--format", `{{.Label "com.docker.compose.project"}}`)
	require.Len(t, projects, 1)
	project := projects[0]
	require.Empty(t, dockerLines(t, "ps", "-aq", "--filter", "label=dev.verity.reaper-for="+project), "--keep must skip Ryuk")
	t.Cleanup(func() { _ = exec.Command(bin, "down", project).Run() })
	require.NoError(t, cmd.Process.Kill())
	_ = syscall.Kill(-group, syscall.SIGKILL)
	require.Error(t, cmd.Wait())
	waited = true

	run := func(args ...string) (string, error) {
		c := exec.CommandContext(ctx, bin, append([]string{"down"}, args...)...)
		c.Dir = t.TempDir() // Cleanup must work without the original Compose file.
		out, err := c.CombinedOutput()
		return string(out), err
	}
	out, err := run()
	require.Error(t, err)
	require.Contains(t, out, project)
	require.NotEmpty(t, dockerLines(t, "ps", "-aq", "--filter", "label=com.docker.compose.project="+project))
	out, err = run("unrelated")
	require.Error(t, err)
	require.Contains(t, out, "refusing")
	out, err = run("--all", "--", project)
	require.Error(t, err)
	require.Equal(t, []string{project}, inventory())
	out, err = run("--", project)
	require.NoError(t, err, out)
	require.Empty(t, leftovers(t, volumesBefore))

	// Network/volume-only projects must be discoverable, while unrelated projects survive.
	orphan := fmt.Sprintf("verity-orphan-%d", time.Now().UnixNano())
	unrelated := "test-" + orphan
	for _, name := range []string{orphan, unrelated} {
		dockerLines(t, "network", "create", "--label", "com.docker.compose.project="+name, "--label", "com.docker.compose.network=default", name)
		t.Cleanup(func() { _ = exec.Command("docker", "network", "rm", name).Run() })
		dockerLines(t, "volume", "create", "--label", "com.docker.compose.project="+name, "--label", "com.docker.compose.volume=data", name)
		t.Cleanup(func() { _ = exec.Command("docker", "volume", "rm", name).Run() })
	}
	out, err = run()
	require.Error(t, err)
	require.Contains(t, out, orphan)
	require.Equal(t, []string{orphan}, inventory(), "another Verity run appeared; refusing --all")
	out, err = run("--all")
	require.NoError(t, err, out)
	require.Empty(t, dockerLines(t, "network", "ls", "-q", "--filter", "label=com.docker.compose.project="+orphan))
	require.Empty(t, dockerLines(t, "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+orphan))
	require.Len(t, dockerLines(t, "network", "ls", "-q", "--filter", "label=com.docker.compose.project="+unrelated), 1)
	require.Len(t, dockerLines(t, "volume", "ls", "-q", "--filter", "label=com.docker.compose.project="+unrelated), 1)
	require.Empty(t, inventory(), "another Verity run appeared; refusing --all")
	out, err = run("--all") // Nothing left is a successful no-op.
	require.NoError(t, err, out)
}
