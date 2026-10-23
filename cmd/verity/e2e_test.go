package main

import (
	"bufio"
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// verityBuild compiles the CLI once per test; the tests run the real binary so
// exit codes and signals behave as they do for users.
func verityBuild(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "verity")
	out, err := exec.Command("go", "build", "-o", bin, ".").CombinedOutput()
	require.NoError(t, err, string(out))
	return bin
}

func dockerLines(t *testing.T, args ...string) []string {
	t.Helper()
	out, err := exec.Command("docker", args...).Output()
	require.NoError(t, err)
	return strings.Fields(string(out))
}

// leftovers lists containers and networks of any verity-* compose project, and
// volumes that were not in volumesBefore. Volumes are compared by name because
// the anonymous ones an image's VOLUME creates carry no compose label.
func leftovers(t *testing.T, volumesBefore []string) []string {
	t.Helper()
	var found []string
	for _, c := range dockerLines(t, "ps", "-a", "--format", `{{.Names}}`) {
		if strings.HasPrefix(c, "verity-") {
			found = append(found, "container "+c)
		}
	}
	for _, n := range dockerLines(t, "network", "ls", "--format", `{{.Name}}`) {
		if strings.HasPrefix(n, "verity-") {
			found = append(found, "network "+n)
		}
	}
	for _, v := range dockerLines(t, "volume", "ls", "-q") {
		if !slices.Contains(volumesBefore, v) {
			found = append(found, "volume "+v)
		}
	}
	return found
}

func fixtureCommand(bin string, env []string, args ...string) *exec.Cmd {
	cmd := exec.Command(bin, append([]string{"run", "--go-dir", "./testdata/fixture", "--docker-compose-file", "testdata/fixture/compose.yaml"}, args...)...)
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

// TestEndToEnd needs a Docker daemon and pulls postgres:17-alpine, so it is opt-in.
// Other Docker activity creating volumes while it runs will fail the volume check.
func TestEndToEnd(t *testing.T) {
	if os.Getenv("VERITY_TEST_DOCKER") == "" {
		t.Skip("set VERITY_TEST_DOCKER=1 to run against a Docker daemon")
	}
	volumesBefore := dockerLines(t, "volume", "ls", "-q")
	require.Empty(t, leftovers(t, volumesBefore), "verity-* containers or networks exist before the test; remove them first")
	bin := verityBuild(t)

	t.Run("pass", func(t *testing.T) {
		out, err := fixtureCommand(bin, nil).CombinedOutput()
		require.NoError(t, err, string(out))
		require.Empty(t, leftovers(t, volumesBefore))
	})

	t.Run("failing test exits non-zero and shows the failure", func(t *testing.T) {
		out, err := fixtureCommand(bin, []string{"FIXTURE_FAIL=1"}).CombinedOutput()
		var exitErr *exec.ExitError
		require.ErrorAs(t, err, &exitErr)
		require.Equal(t, 1, exitErr.ExitCode())
		require.Contains(t, string(out), "asked to fail")
		require.Empty(t, leftovers(t, volumesBefore))
	})

	t.Run("-run selects tests", func(t *testing.T) {
		out, err := fixtureCommand(bin, nil, "--", "-v", "-run", "TestSlow").CombinedOutput()
		require.NoError(t, err, string(out))
		require.Contains(t, string(out), "--- PASS: TestSlow")
		require.NotContains(t, string(out), "TestReachesDB")
	})

	t.Run("SIGINT mid-run cleans up", func(t *testing.T) {
		cmd := fixtureCommand(bin, []string{"FIXTURE_SLEEP=2m"}, "--", "-v", "-run", "TestSlow")
		var stderr, seen bytes.Buffer
		stdout, err := cmd.StdoutPipe()
		require.NoError(t, err)
		cmd.Stderr = &stderr
		require.NoError(t, cmd.Start())

		// The test prints "=== RUN" once it is running, so the stack is up by then.
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			seen.WriteString(sc.Text() + "\n")
			if strings.Contains(sc.Text(), "=== RUN   TestSlow") {
				break
			}
		}
		require.NoError(t, cmd.Process.Signal(os.Interrupt))

		done := make(chan error, 1)
		go func() {
			_, _ = io.Copy(io.Discard, stdout) // drain before Wait closes the pipe
			done <- cmd.Wait()
		}()
		select {
		case <-done:
		case <-time.After(time.Minute):
			t.Fatal("verity did not exit after SIGINT\n" + seen.String())
		}
		require.Empty(t, leftovers(t, volumesBefore))
	})
}
