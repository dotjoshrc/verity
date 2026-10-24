package verity

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/dotjoshrc/verity/internal/core"
)

const maxBackendLogBytes = 64 * 1024

// logBuffer keeps draining the command after reaching the output limit.
type logBuffer struct {
	buffer    bytes.Buffer
	truncated bool
}

func (b *logBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := maxBackendLogBytes - b.buffer.Len()
	if n > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return n, nil
}

func captureFailureLogs(t *testing.T, start time.Time) {
	t.Helper()
	if !t.Failed() {
		return
	}
	d, err := core.DecodeDiscovery(os.Getenv(core.DiscoveryEnv))
	if err != nil || d.Project == "" {
		t.Log("Backend logs skipped: no Compose project in VERITY_DISCOVERY")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", d.Project, "logs", "--since", start.UTC().Format(time.RFC3339Nano), "--no-color", "--tail", "200")
	cmd.WaitDelay = time.Second
	var output logBuffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	if output.buffer.Len() > 0 {
		t.Logf("Backend logs (shared Compose stack):\n%s", output.buffer.String())
	}
	if output.truncated {
		t.Log("Backend logs truncated at 64 KiB")
	}
	if err != nil {
		t.Logf("Backend log capture unavailable: %v", err)
	}
}
