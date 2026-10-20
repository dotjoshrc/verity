package main

import (
	"cmp"
	"fmt"
	"maps"
	"slices"
	"strconv"

	"github.com/compose-spec/compose-go/v2/types"
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

// buildDiscovery maps every service labelled verity.type to an endpoint. The
// host is the compose service name, which resolves on the project network.
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
			Host:     name,
			Port:     port,
			User:     cmp.Or(svc.Labels["verity.user"], user),
			Password: cmp.Or(svc.Labels["verity.password"], password),
			Database: cmp.Or(svc.Labels["verity.database"], database),
		}
	}

	return d, nil
}
