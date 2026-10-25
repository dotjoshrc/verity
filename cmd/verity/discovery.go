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
	resolve func(env func(string) string) core.Endpoint
	// fileVars maps env vars we do not read to the label that replaces them.
	fileVars map[string]string
}

var backendTypes = map[string]backendType{
	"wiremock": {
		port:    8080,
		resolve: func(func(string) string) core.Endpoint { return core.Endpoint{} },
	},
	"postgres": {
		port: 5432,
		fileVars: map[string]string{
			"POSTGRES_USER_FILE":     "verity.user",
			"POSTGRES_PASSWORD_FILE": "verity.password",
			"POSTGRES_DB_FILE":       "verity.database",
		},
		resolve: func(env func(string) string) core.Endpoint {
			user := cmp.Or(env("POSTGRES_USER"), "postgres")
			// The image's POSTGRES_DB defaults to the user, not "postgres".
			return core.Endpoint{User: user, Password: env("POSTGRES_PASSWORD"), Database: cmp.Or(env("POSTGRES_DB"), user)}
		},
	},
	"redis": {
		port: 6379,
		// The official image takes --requirepass on its command line, not from env,
		// so a password can only come from the verity.password label.
		resolve: func(func(string) string) core.Endpoint { return core.Endpoint{User: "default"} },
	},
}

// buildDiscovery maps every service labelled verity.type to an endpoint. Host is
// empty and Port is the service's internal port until fillAddresses runs.
func buildDiscovery(project *types.Project, runID string) (*core.Discovery, error) {
	d := &core.Discovery{Version: core.DiscoveryVersion, RunID: runID, Project: project.Name, Backends: map[string]core.Endpoint{}}

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
		e := bt.resolve(env)
		e.Name, e.Type, e.Port = name, typ, port
		e.User = cmp.Or(svc.Labels["verity.user"], e.User)
		e.Password = cmp.Or(svc.Labels["verity.password"], e.Password)
		e.Database = cmp.Or(svc.Labels["verity.database"], e.Database)
		for label, value := range svc.Labels {
			if key, ok := strings.CutPrefix(label, "verity.opt."); ok && key != "" {
				if e.Extra == nil {
					e.Extra = map[string]string{}
				}
				e.Extra[key] = value
			}
		}
		d.Backends[name] = e
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

// addHostGateway lets containers call services running on the host.
func addHostGateway(project *types.Project) {
	for name, svc := range project.Services {
		// Shared network namespaces reject extra_hosts; the owning container supplies DNS.
		if strings.HasPrefix(svc.NetworkMode, "service:") || strings.HasPrefix(svc.NetworkMode, "container:") {
			continue
		}
		if _, exists := svc.ExtraHosts["host.docker.internal"]; exists {
			continue
		}
		if svc.ExtraHosts == nil {
			svc.ExtraHosts = types.HostsList{}
		}
		svc.ExtraHosts["host.docker.internal"] = []string{"host-gateway"}
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
