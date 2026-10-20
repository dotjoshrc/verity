package verity

import (
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func getCaller(t *testing.T) string {
	pc, _, _, _ := runtime.Caller(2)
	funcName, err := url.QueryUnescape(runtime.FuncForPC(pc).Name())
	require.NoError(t, err)

	nameParts := strings.Split(funcName, "/")

	// Default to the last part.
	name := nameParts[len(nameParts)-1]

	info, ok := debug.ReadBuildInfo()
	if ok {
		name = strings.TrimPrefix(funcName, info.Main.Path+"/")
	}

	name = strings.ToLower(regexp.MustCompile("[^a-zA-Z0-9]+").ReplaceAllString(name, "_"))

	return name
}

func generateID() string {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	if err != nil {
		panic(err)
	}

	return hex.EncodeToString(b)
}
