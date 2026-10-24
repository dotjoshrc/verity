package main

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestHostGatewayCallback(t *testing.T) {
	if os.Getenv("VERITY_TEST_DOCKER") == "" {
		t.Skip("set VERITY_TEST_DOCKER=1 to run against a Docker daemon")
	}
	listener, err := net.Listen("tcp4", "0.0.0.0:0")
	require.NoError(t, err)
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("callback reached host"))
	}), ReadHeaderTimeout: time.Second}
	t.Cleanup(func() { _ = server.Close() })
	go func() { _ = server.Serve(listener) }()
	project := loadFixture(t, fmt.Sprintf(`
services:
  callback:
    image: busybox:1.37
    command: [wget, -qO-, -T, "5", "http://host.docker.internal:%d"]
`, listener.Addr().(*net.TCPAddr).Port))
	addHostGateway(project)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "docker", "compose", "-p", fmt.Sprintf("verity-callback-%d", time.Now().UnixNano()), "-f", "-", "run", "--rm", "--no-deps", "callback")
	// No network is created or left behind by the one-off container.
	svc := project.Services["callback"]
	svc.NetworkMode = "bridge"
	svc.Networks = nil
	project.Services["callback"] = svc
	config, err := project.MarshalYAML()
	require.NoError(t, err)
	cmd.Stdin = bytes.NewReader(config)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, string(out))
	require.Contains(t, string(out), "callback reached host")
}
