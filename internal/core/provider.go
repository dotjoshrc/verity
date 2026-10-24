// Package core holds the types that cross verity's process and package
// boundaries. It must not import testing.
package core

import (
	"context"
	"fmt"
	"sync"
)

// Endpoint is a discovered backend, addressed from the host.
type Endpoint struct {
	Extra    map[string]string `json:"extra,omitempty"`
	Name     string            `json:"-"` // the compose service name; the key in Discovery.Backends
	Type     string            `json:"type"`
	Host     string            `json:"host"`
	Port     int               `json:"port"`
	User     string            `json:"user"`
	Password string            `json:"password"`
	Database string            `json:"database"`
}

// Request asks a Provider for an isolated slice of a backend.
type Request struct {
	RunID, SuiteID string
	Endpoint       Endpoint
	Options        []any // provider-specific options passed to verity.Use
}

// Resource is a provisioned slice of a backend.
type Resource struct {
	Env     map[string]string           // for example DATABASE_URL=...; feeds the runner
	Handle  any                         // typed access, for example a *sql.DB on the slice
	Release func(context.Context) error // optional
}

// Provider provisions slices of one backend type.
type Provider interface {
	Provision(ctx context.Context, req Request) (*Resource, error)
}

var (
	mu        sync.RWMutex
	providers = map[string]Provider{}
)

// RegisterProvider registers p as the provider for backend type typ. It panics
// on a nil provider or a duplicate type, like database/sql.Register.
func RegisterProvider(typ string, p Provider) {
	mu.Lock()
	defer mu.Unlock()

	if p == nil {
		panic("verity: RegisterProvider provider is nil")
	}
	if _, dup := providers[typ]; dup {
		panic(fmt.Sprintf("verity: RegisterProvider called twice for type %q", typ))
	}
	providers[typ] = p
}

// LookupProvider returns the provider registered for typ.
func LookupProvider(typ string) (Provider, bool) {
	mu.RLock()
	defer mu.RUnlock()

	p, ok := providers[typ]
	return p, ok
}
