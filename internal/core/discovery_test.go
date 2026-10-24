package core

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDiscoveryCompatibility(t *testing.T) {
	for _, extra := range []bool{false, true} {
		d := Discovery{RunID: "run1", Backends: map[string]Endpoint{"db": {Type: "postgres", Host: "127.0.0.1", Port: 5432}}}
		if extra {
			d.Project = "verity-run1"
			e := d.Backends["db"]
			e.Extra = map[string]string{"setting": "value"}
			d.Backends["db"] = e
		}
		encoded, err := d.Encode()
		require.NoError(t, err)
		decoded, err := DecodeDiscovery(encoded)
		require.NoError(t, err)
		require.Equal(t, d.Project, decoded.Project)
		require.Equal(t, d.Backends["db"].Extra, decoded.Backends["db"].Extra)
		require.Equal(t, "db", decoded.Backends["db"].Name)
		if !extra {
			require.NotContains(t, encoded, `"project"`)
			require.NotContains(t, encoded, `"extra"`)
		}
		// A version-1 reader without the new fields still reads the endpoint.
		var old struct {
			Version  int `json:"version"`
			Backends map[string]struct {
				Type string `json:"type"`
				Port int    `json:"port"`
			} `json:"backends"`
		}
		require.NoError(t, json.Unmarshal([]byte(encoded), &old))
		require.Equal(t, 1, old.Version)
		require.Equal(t, "postgres", old.Backends["db"].Type)
		require.Equal(t, 5432, old.Backends["db"].Port)
	}
}
