package core

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DiscoveryEnv is the env var the CLI sets in each test process.
const DiscoveryEnv = "VERITY_DISCOVERY"

// DiscoveryVersion is the only discovery protocol version this package reads.
const DiscoveryVersion = 1

// Discovery tells a test process which backends exist and how to reach them.
type Discovery struct {
	Project  string              `json:"project,omitempty"`
	Version  int                 `json:"version"`
	RunID    string              `json:"run_id"`
	Backends map[string]Endpoint `json:"backends"`
}

// Encode returns the JSON for the discovery env var.
func (d Discovery) Encode() (string, error) {
	d.Version = DiscoveryVersion
	b, err := json.Marshal(d)
	return string(b), err
}

// DecodeDiscovery parses the value of the discovery env var.
func DecodeDiscovery(s string) (*Discovery, error) {
	if s == "" {
		return nil, errors.New(DiscoveryEnv + " is not set: run the tests through `verity run`")
	}

	var d Discovery
	if err := json.Unmarshal([]byte(s), &d); err != nil {
		return nil, fmt.Errorf("parse %s: %w", DiscoveryEnv, err)
	}
	if d.Version != DiscoveryVersion {
		return nil, fmt.Errorf("%s has unsupported version %d (want %d): run the tests through a matching `verity run`", DiscoveryEnv, d.Version, DiscoveryVersion)
	}

	for name, e := range d.Backends {
		e.Name = name
		d.Backends[name] = e
	}

	return &d, nil
}
