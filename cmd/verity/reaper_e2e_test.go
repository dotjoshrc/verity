package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReaperDocker(t *testing.T) {
	if os.Getenv("VERITY_TEST_DOCKER") == "" {
		t.Skip("set VERITY_TEST_DOCKER=1 to run against Docker")
	}
	bin := verityBuild(t)
	for _, killGroup := range []bool{false, true} {
		name := "SIGKILL CLI"
		if killGroup {
			name = "SIGKILL CLI process group"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "verity.log")
			output, err := os.Create(path)
			require.NoError(t, err)
			t.Cleanup(func() { _ = output.Close() })
			cmd := fixtureCommand(bin, []string{"FIXTURE_SLEEP=2m", "FIXTURE_REPORT_PGID=1"}, "--", "-v", "-run", "TestSlow")
			cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
			cmd.Stdout, cmd.Stderr = output, output
			require.NoError(t, cmd.Start())
			t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
			var project string
			var group int
			require.Eventually(t, func() bool {
				data, _ := os.ReadFile(path)
				for _, line := range strings.Split(string(data), "\n") {
					if p, ok := strings.CutPrefix(line, "fixture project: "); ok {
						project = p
					}
					if p, ok := strings.CutPrefix(line, "fixture process group: "); ok {
						group, _ = strconv.Atoi(p)
					}
				}
				return project != "" && group != 0
			}, 30*time.Second, 100*time.Millisecond, "fixture did not start; see %s", path)
			t.Cleanup(func() { _ = syscall.Kill(-group, syscall.SIGKILL) })
			t.Cleanup(func() { _ = exec.Command(bin, "down", project).Run() })
			reaper := project + "-reaper"
			require.Len(t, dockerLines(t, "ps", "-q", "--filter", "label=dev.verity.reaper-for="+project), 1)
			// Inspect both named and image-created anonymous volumes before the kill.
			db := dockerLines(t, "ps", "-q", "--filter", "label=com.docker.compose.project="+project, "--filter", "label=com.docker.compose.service=db")
			require.Len(t, db, 1)
			volumes := dockerLines(t, "inspect", db[0], "--format", `{{range .Mounts}}{{if eq .Type "volume"}}{{.Name}} {{end}}{{end}}`)
			require.Len(t, volumes, 2)
			if killGroup {
				require.NoError(t, syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL))
			} else {
				require.NoError(t, cmd.Process.Kill())
			}
			require.Error(t, cmd.Wait())
			require.Eventually(t, func() bool {
				for _, args := range [][]string{
					{"ps", "-aq", "--filter", "label=com.docker.compose.project=" + project},
					{"network", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project},
					{"volume", "ls", "-q", "--filter", "label=com.docker.compose.project=" + project},
					{"ps", "-aq", "--filter", "name=^/" + reaper + "$"},
				} {
					if len(dockerLines(t, args...)) > 0 {
						return false
					}
				}
				for _, volume := range volumes {
					if exec.Command("docker", "volume", "inspect", volume).Run() == nil {
						return false
					}
				}
				return true
			}, 30*time.Second, 200*time.Millisecond, "Ryuk did not clean project %s", project)
		})
	}
}
