package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/cli/cli/context/docker"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/client"
	"github.com/stretchr/testify/require"
)

func TestReaperSocket(t *testing.T) {
	for _, tc := range []struct{ host, os, want string }{
		{"unix:///var/run/docker.sock", "Linux", "/var/run/docker.sock"},
		{"unix:///run/user/1000/docker.sock", "Linux", "/run/user/1000/docker.sock"},
		{"unix:///Users/test/.docker/run/docker.sock", "Docker Desktop", "/var/run/docker.sock"},
	} {
		got, err := reaperSocket(tc.host, tc.os)
		require.NoError(t, err)
		require.Equal(t, tc.want, got)
	}
	for _, host := range []string{"tcp://example:2375", "ssh://host", "unix://relative", ""} {
		_, err := reaperSocket(host, "Linux")
		require.Error(t, err)
	}
}

func TestReaperHandshake(t *testing.T) {
	for _, reply := range []string{"ACK\n", "NO\n", "oversized-reply-that-never-ends"} {
		t.Run(reply, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			registered := make(chan string, 1)
			closed := make(chan struct{})
			go func() {
				defer close(closed)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				line, _ := bufio.NewReader(conn).ReadString('\n')
				registered <- line
				_, _ = fmt.Fprint(conn, reply)
				_, _ = io.Copy(io.Discard, conn)
			}()
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			conn, err := connectReaper(ctx, listener.Addr().String(), "verity-abc123")
			if reply == "ACK\n" {
				require.NoError(t, err)
				cancel() // Startup context must not close the connection once registered.
				select {
				case <-closed:
					t.Fatal("connection closed with startup context")
				case <-time.After(10 * time.Millisecond):
				}
				require.NoError(t, conn.Close())
			} else {
				require.Error(t, err)
			}
			filter, err := url.ParseQuery(strings.TrimSuffix(<-registered, "\n"))
			require.NoError(t, err)
			require.Equal(t, api.ProjectLabel+"=verity-abc123", filter.Get("label"))
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("connection leaked")
			}
		})
	}
}

func TestReaperHandshakeCancellation(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		_, _ = io.Copy(io.Discard, conn) // Never acknowledge.
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	conn, err := connectReaper(ctx, listener.Addr().String(), "verity-abc123")
	require.Error(t, err)
	require.Nil(t, conn)
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("connection leaked")
	}
}

type reaperClient struct {
	client.APIClient
	removed    bool
	created    client.ContainerCreateOptions
	inspectErr error
	pulled     bool
}

func (c *reaperClient) ContainerRemove(_ context.Context, id string, opts client.ContainerRemoveOptions) (client.ContainerRemoveResult, error) {
	c.removed = id == "reaper" && opts.Force
	return client.ContainerRemoveResult{}, nil
}

func TestReaperClose(t *testing.T) {
	for _, cleaned := range []bool{false, true} {
		c := &reaperClient{}
		conn, peer := net.Pipe()
		r := &reaper{docker: c, id: "reaper", conn: conn}
		r.close(cleaned)
		require.Equal(t, cleaned, c.removed)
		_, err := peer.Read(make([]byte, 1))
		require.True(t, errors.Is(err, io.EOF))
		_ = peer.Close()
	}
}

func TestReaperRetriesUntilReady(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; attempt < 2; attempt++ {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_, _ = bufio.NewReader(conn).ReadString('\n')
			if attempt == 1 {
				_, _ = fmt.Fprint(conn, "ACK\n")
			}
			_ = conn.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	conn, err := connectReaper(ctx, listener.Addr().String(), "verity-abc123")
	require.NoError(t, err)
	_ = conn.Close()
	<-done
}

type reaperCLI struct {
	command.Cli
	engine *reaperClient
}

func (c reaperCLI) Client() client.APIClient { return c.engine }
func (c reaperCLI) DockerEndpoint() docker.Endpoint {
	return docker.Endpoint{EndpointMeta: docker.EndpointMeta{Host: "unix:///var/run/docker.sock"}}
}
func (c *reaperClient) Info(context.Context, client.InfoOptions) (client.SystemInfoResult, error) {
	var info client.SystemInfoResult
	info.Info.OperatingSystem = "Linux"
	return info, nil
}
func (c *reaperClient) ImageInspect(context.Context, string, ...client.ImageInspectOption) (client.ImageInspectResult, error) {
	return client.ImageInspectResult{}, c.inspectErr
}
func (c *reaperClient) ContainerCreate(_ context.Context, opts client.ContainerCreateOptions) (client.ContainerCreateResult, error) {
	c.created = opts
	return client.ContainerCreateResult{ID: "reaper"}, nil
}
func (c *reaperClient) ContainerStart(context.Context, string, client.ContainerStartOptions) (client.ContainerStartResult, error) {
	return client.ContainerStartResult{}, errors.New("startup denied")
}
func TestReaperStartupFailureCleansContainer(t *testing.T) {
	engine := &reaperClient{}
	r, err := startReaper(context.Background(), reaperCLI{engine: engine}, "verity-abc123")
	require.ErrorContains(t, err, "startup denied")
	require.Nil(t, r)
	require.True(t, engine.removed)
	require.True(t, engine.created.HostConfig.AutoRemove)
	require.Equal(t, "/var/run/docker.sock", engine.created.HostConfig.Mounts[0].Source)
	require.Empty(t, engine.created.Config.Labels[api.ProjectLabel], "reaper must not match its cleanup filter")
}

func (c *reaperClient) ImagePull(context.Context, string, client.ImagePullOptions) (client.ImagePullResponse, error) {
	c.pulled = true
	return nil, errors.New("registry unavailable")
}
func TestReaperImageFailure(t *testing.T) {
	for _, missing := range []bool{false, true} {
		engine := &reaperClient{inspectErr: errors.New("inspect denied")}
		want := "inspect Ryuk image"
		if missing {
			engine.inspectErr = errdefs.ErrNotFound
			want = "registry unavailable"
		}
		r, err := startReaper(context.Background(), reaperCLI{engine: engine}, "verity-abc123")
		require.ErrorContains(t, err, want)
		require.Nil(t, r)
		require.Equal(t, missing, engine.pulled)
		require.Nil(t, engine.created.Config, "must not create resources when the image is unavailable")
	}
}
