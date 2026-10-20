package core

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryRoundTrip(t *testing.T) {
	want := Discovery{RunID: "k3f9a2", Backends: map[string]Endpoint{
		"db": {Name: "db", Type: "postgres", Host: "db", Port: 5432, User: "postgres", Password: "postgres", Database: "postgres"},
	}}

	s, err := want.Encode()
	require.NoError(t, err)
	require.JSONEq(t, `{"version":1,"run_id":"k3f9a2","backends":{"db":{"type":"postgres","host":"db","port":5432,"user":"postgres","password":"postgres","database":"postgres"}}}`, s)

	got, err := DecodeDiscovery(s)
	require.NoError(t, err)
	want.Version = DiscoveryVersion
	require.Equal(t, &want, got)
}

func TestDecodeDiscoveryErrors(t *testing.T) {
	for name, in := range map[string]string{
		"empty":       "",
		"bad json":    "{",
		"no version":  `{"run_id":"x"}`,
		"old version": `{"version":2}`,
	} {
		t.Run(name, func(t *testing.T) {
			_, err := DecodeDiscovery(in)
			require.ErrorContains(t, err, DiscoveryEnv)
		})
	}
}

type fakeProvider struct{}

func (fakeProvider) Provision(context.Context, Request) (*Resource, error) { return nil, nil }

func TestRegisterProvider(t *testing.T) {
	RegisterProvider("fake", fakeProvider{})
	t.Cleanup(func() { // the registry is process-global; keep -count=N runs independent
		mu.Lock()
		delete(providers, "fake")
		mu.Unlock()
	})

	p, ok := LookupProvider("fake")
	require.True(t, ok)
	require.Equal(t, fakeProvider{}, p)

	_, ok = LookupProvider("missing")
	require.False(t, ok)

	require.Panics(t, func() { RegisterProvider("fake", fakeProvider{}) })
	require.Panics(t, func() { RegisterProvider("nil", nil) })
}
