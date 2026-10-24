package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/log"
	"github.com/containerd/errdefs"
	"github.com/docker/cli/cli/command"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

const ryukImage = "testcontainers/ryuk:0.14.0@sha256:7c1a8a9a47c780ed0f983770a662f80deb115d95cce3e2daa3d12115b8cd28f0"

// startReaper must acknowledge the exact project before any stack resources exist.
func startReaper(ctx context.Context, docker command.Cli, project string) (*reaper, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	engine := docker.Client()
	info, err := engine.Info(ctx, client.InfoOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect Docker daemon: %w", err)
	}
	socket, err := reaperSocket(docker.DockerEndpoint().Host, info.Info.OperatingSystem)
	if err != nil {
		return nil, err
	}
	if _, err := engine.ImageInspect(ctx, ryukImage); err != nil {
		if !errdefs.IsNotFound(err) {
			return nil, fmt.Errorf("inspect Ryuk image: %w", err)
		}
		log.Infof("pulling cleanup reaper %s", ryukImage)
		pull, err := engine.ImagePull(ctx, ryukImage, client.ImagePullOptions{})
		if err != nil {
			return nil, fmt.Errorf("pull Ryuk image: %w", err)
		}
		if err := pull.Wait(ctx); err != nil {
			return nil, fmt.Errorf("pull Ryuk image: %w", err)
		}
	}
	port := network.MustParsePort("8080/tcp")
	created, err := engine.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name:   project + "-reaper",
		Config: &container.Config{Image: ryukImage, ExposedPorts: network.PortSet{port: {}}, Labels: map[string]string{"dev.verity.reaper-for": project}},
		HostConfig: &container.HostConfig{AutoRemove: true,
			Mounts:       []mount.Mount{{Type: mount.TypeBind, Source: socket, Target: "/var/run/docker.sock"}},
			PortBindings: network.PortMap{port: {{HostIP: netip.MustParseAddr("127.0.0.1")}}},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create Ryuk: %w", err)
	}
	r := &reaper{docker: engine, id: created.ID}
	ready := false
	defer func() {
		if !ready {
			r.close(true)
		}
	}()
	if _, err := engine.ContainerStart(ctx, r.id, client.ContainerStartOptions{}); err != nil {
		return nil, fmt.Errorf("start Ryuk: %w", err)
	}
	inspected, err := engine.ContainerInspect(ctx, r.id, client.ContainerInspectOptions{})
	if err != nil {
		return nil, fmt.Errorf("inspect Ryuk: %w", err)
	}
	bindings := inspected.Container.NetworkSettings.Ports[port]
	if len(bindings) != 1 || bindings[0].HostPort == "" {
		return nil, errors.New("Ryuk has no published loopback port")
	}
	handshakeCtx, handshakeCancel := context.WithTimeout(ctx, 30*time.Second)
	defer handshakeCancel()
	r.conn, err = connectReaper(handshakeCtx, net.JoinHostPort("127.0.0.1", bindings[0].HostPort), project)
	if err != nil {
		return nil, fmt.Errorf("register Ryuk cleanup: %w", err)
	}
	ready = true
	return r, nil
}

func reaperSocket(host, operatingSystem string) (string, error) {
	if !strings.HasPrefix(host, "unix://") {
		return "", errors.New("Ryuk requires a local Unix Docker socket; remote Docker daemons are unsupported")
	}
	if operatingSystem == "Docker Desktop" {
		return "/var/run/docker.sock", nil
	}
	socket := strings.TrimPrefix(host, "unix://")
	if !filepath.IsAbs(socket) {
		return "", fmt.Errorf("Docker socket path must be absolute: %q", socket)
	}
	return socket, nil
}

func connectReaper(ctx context.Context, address, project string) (net.Conn, error) {
	filter := url.Values{"label": {api.ProjectLabel + "=" + project}}.Encode() + "\n"
	for {
		conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", address)
		if err == nil {
			stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			_, err = fmt.Fprint(conn, filter)
			var reply []byte
			if err == nil {
				reply, err = bufio.NewReaderSize(conn, 16).ReadSlice('\n')
			}
			stopped := stop()
			if err == nil && string(reply) == "ACK\n" && stopped && ctx.Err() == nil {
				if err := conn.SetDeadline(time.Time{}); err != nil {
					_ = conn.Close()
					return nil, err
				}
				return conn, nil
			}
			_ = conn.Close()
			if err == nil && string(reply) != "ACK\n" {
				return nil, fmt.Errorf("unexpected Ryuk reply %q", reply)
			}
			if errors.Is(err, bufio.ErrBufferFull) {
				return nil, err
			}
		}
		// Docker may publish the port before Ryuk starts accepting registrations.
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type reaper struct {
	docker client.APIClient
	id     string
	conn   net.Conn
}

// After successful Down, remove Ryuk immediately. Otherwise leave it to reap.
func (r *reaper) close(cleaned bool) {
	if r.conn != nil {
		_ = r.conn.Close()
	}
	if !cleaned {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := r.docker.ContainerRemove(ctx, r.id, client.ContainerRemoveOptions{Force: true}); err != nil && !errdefs.IsNotFound(err) {
		log.Errorf("remove Ryuk: %v", err)
	}
}
