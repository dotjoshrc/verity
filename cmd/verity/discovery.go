package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/types"
	"github.com/docker/compose/v5/pkg/api"
	"github.com/dotjoshrc/verity/internal/core"
)

// backendType holds the per-type defaults used to build discovery. This is the
// only backend-specific knowledge in the CLI.
type backendType struct {
	port int
	// resolve returns the admin credentials from the service's standard env.
	resolve func(env func(string) string) (user, password, database string)
	// fileVars maps env vars we do not read to the label that replaces them.
	fileVars map[string]string
}

var backendTypes = map[string]backendType{
	"postgres": {
		port: 5432,
		fileVars: map[string]string{
			"POSTGRES_USER_FILE":     "verity.user",
			"POSTGRES_PASSWORD_FILE": "verity.password",
			"POSTGRES_DB_FILE":       "verity.database",
		},
		resolve: func(env func(string) string) (string, string, string) {
			user := cmp.Or(env("POSTGRES_USER"), "postgres")
			// The image's POSTGRES_DB defaults to the user, not "postgres".
			return user, env("POSTGRES_PASSWORD"), cmp.Or(env("POSTGRES_DB"), user)
		},
	},
}

// buildDiscovery maps every service labelled verity.type to an endpoint. Host is
// empty and Port is the service's internal port until fillAddresses runs.
func buildDiscovery(project *types.Project, runID string) (*core.Discovery, error) {
	d := &core.Discovery{Version: core.DiscoveryVersion, RunID: runID, Backends: map[string]core.Endpoint{}}

	for _, name := range slices.Sorted(maps.Keys(project.Services)) { // sorted so errors are deterministic
		svc := project.Services[name]
		typ := svc.Labels["verity.type"]
		if typ == "" {
			continue
		}

		bt, ok := backendTypes[typ]
		if !ok {
			return nil, fmt.Errorf("service %q: unknown verity.type %q", name, typ)
		}

		// Docker rejects published ports under these modes, with a confusing error.
		if m := svc.NetworkMode; m == "host" || m == "none" || strings.HasPrefix(m, "service:") || strings.HasPrefix(m, "container:") {
			return nil, fmt.Errorf("service %q: network_mode %q cannot publish a port for verity", name, m)
		}

		port := bt.port
		if v := svc.Labels["verity.port"]; v != "" {
			p, err := strconv.Atoi(v)
			if err != nil || p < 1 || p > 65535 {
				return nil, fmt.Errorf("service %q: verity.port %q is not a port number", name, v)
			}
			port = p
		}

		// A key with no value (list form `- POSTGRES_USER`) is a nil pointer.
		env := func(key string) string {
			if v := svc.Environment[key]; v != nil {
				return *v
			}
			return ""
		}
		for _, v := range slices.Sorted(maps.Keys(bt.fileVars)) {
			if label := bt.fileVars[v]; env(v) != "" && svc.Labels[label] == "" {
				return nil, fmt.Errorf("service %q: %s is not supported; set the %s label instead", name, v, label)
			}
		}

		// Labels override only when non-empty.
		user, password, database := bt.resolve(env)

		d.Backends[name] = core.Endpoint{
			Name:     name,
			Type:     typ,
			Port:     port,
			User:     cmp.Or(svc.Labels["verity.user"], user),
			Password: cmp.Or(svc.Labels["verity.password"], password),
			Database: cmp.Or(svc.Labels["verity.database"], database),
		}
	}

	return d, nil
}

// publishLoopback replaces the ports of every labelled service with one
// ephemeral port on 127.0.0.1, targeting its internal port.
func publishLoopback(project *types.Project, d *core.Discovery) {
	for name, e := range d.Backends {
		svc := project.Services[name]
		svc.Ports = []types.ServicePortConfig{{HostIP: "127.0.0.1", Target: uint32(e.Port), Protocol: "tcp"}}
		project.Services[name] = svc
	}
}

// fillAddresses points every endpoint at the host port compose assigned.
func fillAddresses(d *core.Discovery, containers []api.ContainerSummary) error {
	for name, e := range d.Backends {
		for _, c := range containers {
			if c.Service != name {
				continue
			}
			target := e.Port
			for _, p := range c.Publishers {
				if p.TargetPort == target && p.PublishedPort != 0 {
					e.Host, e.Port = "127.0.0.1", p.PublishedPort
				}
			}
		}
		if e.Host == "" {
			return fmt.Errorf("service %q: no published port for %d", name, e.Port)
		}
		d.Backends[name] = e
	}
	return nil
}

// requireHealthchecks rejects labelled services without a healthcheck block:
// without one, "running" is the only readiness signal and tests start early.
// shortcut: an image's built-in HEALTHCHECK is invisible here, so it is rejected too.
func requireHealthchecks(project *types.Project, d *core.Discovery) error {
	for _, name := range slices.Sorted(maps.Keys(d.Backends)) {
		if hc := project.Services[name].HealthCheck; hc == nil || hc.Disable || (len(hc.Test) > 0 && hc.Test[0] == "NONE") {
			return fmt.Errorf("service %q: labelled services need a healthcheck in the compose file", name)
		}
	}
	return nil
}
