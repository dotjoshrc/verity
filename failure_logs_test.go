package verity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/dotjoshrc/verity/internal/core"
	"github.com/stretchr/testify/require"
)

type logCaptureSuite struct{ Suite }

func (s *logCaptureSuite) TestCapture(t *testing.T) {
	if os.Getenv("VERITY_LOG_HELPER") == "pass" {
		return
	}
	if os.Getenv("VERITY_LOG_HELPER") == "real" {
		d, err := core.DecodeDiscovery(os.Getenv(core.DiscoveryEnv))
		require.NoError(t, err)
		out, err := exec.Command("docker", "compose", "-p", d.Project, "exec", "-T", "logger", "sh", "-c", "echo captured-backend-marker >/proc/1/fd/1").CombinedOutput()
		require.NoError(t, err, string(out))
	}
	t.Cleanup(func() { t.Log("method cleanup ran") })
	t.Fatal("original test failure")
}

func TestFailureLogHelper(t *testing.T) {
	if os.Getenv("VERITY_LOG_HELPER") == "" {
		t.Skip("subprocess only")
	}
	RunSuite(NewSuite[logCaptureSuite](t))
}

func runLogHelper(mode, path, discovery string) (string, error) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestFailureLogHelper$", "-test.v", "-test.count=1")
	cmd.Env = append(os.Environ(), "VERITY_LOG_HELPER="+mode, "PATH="+path, core.DiscoveryEnv+"="+discovery)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestAutomaticFailureLogs(t *testing.T) {
	discovery := `{"version":1,"project":"verity-logs","backends":{}}`
	for _, tc := range []struct {
		name, mode, discovery, script, want string
		success                             bool
	}{
		{name: "failure", mode: "fail", discovery: discovery, script: `printf '%s\n' "$@"; echo backend-marker`, want: "backend-marker"},
		{name: "passing", mode: "pass", discovery: discovery, script: `echo unexpected-capture; exit 1`, success: true},
		{name: "no docker", mode: "fail", discovery: discovery, want: "Backend log capture unavailable:"},
		{name: "old CLI", mode: "fail", discovery: `{"version":1}`, want: "Backend logs skipped: no Compose project"},
		{name: "command error", mode: "fail", discovery: discovery, script: `echo daemon-unavailable; exit 1`, want: "Backend log capture unavailable:"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.script != "" {
				require.NoError(t, os.WriteFile(filepath.Join(dir, "docker"), []byte("#!/bin/sh\n"+tc.script+"\n"), 0700))
			}
			out, err := runLogHelper(tc.mode, dir, tc.discovery)
			if tc.success {
				require.NoError(t, err, out)
				require.NotContains(t, out, "Backend log")
				require.NotContains(t, out, "unexpected-capture")
			} else {
				require.Error(t, err, out)
				require.Contains(t, out, "original test failure")
				require.Contains(t, out, tc.want)
			}
			if tc.name == "failure" {
				require.Contains(t, out, "--since")
				require.Contains(t, out, "--tail\n        200")
				require.Less(t, bytes.Index([]byte(out), []byte("method cleanup ran")), bytes.Index([]byte(out), []byte("Backend logs (")))
			}
		})
	}
}

func TestBackendLogLimit(t *testing.T) {
	var b logBuffer
	data := bytes.Repeat([]byte("x"), maxBackendLogBytes+1)
	n, err := io.Copy(&b, struct{ io.Reader }{bytes.NewReader(data)})
	require.NoError(t, err)
	require.Equal(t, int64(len(data)), n)
	require.Equal(t, maxBackendLogBytes, b.buffer.Len())
	require.True(t, b.truncated)
	written, err := b.Write([]byte("more"))
	require.NoError(t, err)
	require.Equal(t, 4, written)
	require.Equal(t, maxBackendLogBytes, b.buffer.Len())
}

func TestFailureLogsDocker(t *testing.T) {
	if os.Getenv("VERITY_TEST_DOCKER") == "" {
		t.Skip("set VERITY_TEST_DOCKER=1 to run against Docker")
	}
	project := fmt.Sprintf("test-verity-logs-%d", time.Now().UnixNano())
	config := "services:\n  logger:\n    image: busybox:1.37\n    command: [sleep, '120']\n"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(ctx, "docker", "compose", "-p", project, "down", "--timeout", "1").CombinedOutput()
		require.NoError(t, err, string(out))
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", project, "-f", "-", "up", "-d")
	cmd.Stdin = bytes.NewBufferString(config)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	discovery, err := (core.Discovery{Project: project}).Encode()
	require.NoError(t, err)
	result, err := runLogHelper("real", os.Getenv("PATH"), discovery)
	require.Error(t, err, result)
	require.Contains(t, result, "original test failure")
	require.Contains(t, result, "Backend logs (shared Compose stack):")
	require.Contains(t, result, "logger-1  | captured-backend-marker")
}
