package verity

import (
	"net/url"
	"regexp"
	"runtime"
	"runtime/debug"
	"strings"
	"testing"
)

func getCaller(t *testing.T) string {
	pc, _, _, _ := runtime.Caller(2)
	funcName, err := url.QueryUnescape(runtime.FuncForPC(pc).Name())
	if err != nil {
		t.Fatalf("decode caller name: %v", err)
	}

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
