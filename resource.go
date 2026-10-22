package verity

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dotjoshrc/verity/internal/core"
)

// releaseTimeout bounds each Release; the test's own context is already cancelled
// by the time cleanups run.
const releaseTimeout = 30 * time.Second

// Get returns the resource that Use provisioned for the backend called name, as
// type T. It fails the suite's test if there is no such resource or it is not a T.
// It fails on the suite's T, so call it from NewSuite's caller or a BeforeSuite
// hook; failing inside a test method makes Go report that a subtest called
// FailNow on its parent.
func Get[T any](s TestSuite, name string) T {
	t := s.T()
	t.Helper()

	h, ok := s.handle(name)
	if !ok {
		t.Fatalf("verity.Get(%q): no such resource; pass verity.Use(%q) to NewSuite", name, name)
	}

	v, ok := h.(T)
	if !ok {
		t.Fatalf("verity.Get(%q): resource is %T, not %s", name, h, reflect.TypeFor[T]())
	}

	return v
}

type plan struct {
	use
	provider core.Provider
	endpoint core.Endpoint
}

// provisionUses provisions every use concurrently, stores the handles and merged
// env on the suite, and returns the env. Releases are registered with t.Cleanup in Use order, so
// they run in reverse, and run even when this fails part way.
func provisionUses(t *testing.T, suite TestSuite, disc *core.Discovery, uses []use) map[string]string {
	t.Helper()

	plans := make([]plan, len(uses))
	seen := map[string]bool{}
	for i, u := range uses {
		if seen[u.name] {
			t.Fatalf("verity.Use(%q) is used twice", u.name)
		}
		seen[u.name] = true

		ep, ok := disc.Backends[u.name]
		if !ok {
			t.Fatalf("verity.Use(%q): no such backend; available: %s", u.name, strings.Join(slices.Sorted(maps.Keys(disc.Backends)), ", "))
		}

		p, ok := core.LookupProvider(ep.Type)
		if !ok {
			t.Fatalf("verity.Use(%q): no provider for type %q; import its package, for example github.com/dotjoshrc/verity/providers/%s", u.name, ep.Type, ep.Type)
		}

		plans[i] = plan{use: u, provider: p, endpoint: ep}
	}

	results := make([]*core.Resource, len(plans))
	errs := make([]error, len(plans))

	var wg sync.WaitGroup
	for i, p := range plans {
		wg.Add(1)
		go func() { // only collects errors: Fatal must come from the test goroutine
			defer wg.Done()
			defer func() { // a panic here would end the binary before any cleanup runs
				if r := recover(); r != nil {
					errs[i] = fmt.Errorf("provider panicked: %v", r)
				}
			}()

			res, err := p.provider.Provision(t.Context(), core.Request{
				RunID: disc.RunID, SuiteID: suite.ID(), Endpoint: p.endpoint, Options: p.opts,
			})
			if err == nil && res == nil {
				err = errors.New("provider returned no resource")
			}
			results[i], errs[i] = res, err
		}()
	}
	wg.Wait()

	// Register releases before failing, so a partial failure still releases what was created.
	var failed []error
	for i, res := range results {
		if errs[i] != nil {
			failed = append(failed, fmt.Errorf("verity.Use(%q): %w", plans[i].name, errs[i]))
			continue
		}
		t.Cleanup(releaser(t, plans[i].name, res))
	}
	if err := errors.Join(failed...); err != nil {
		t.Fatalf("provision: %v", err)
	}

	handles := map[string]any{}
	env := map[string]string{}
	owner := map[string]string{}
	for i, res := range results {
		handles[plans[i].name] = res.Handle

		for _, k := range slices.Sorted(maps.Keys(res.Env)) {
			if prev, dup := owner[k]; dup {
				t.Fatalf("env var %s is set by both %q and %q; give one of them a different name (for example postgres.EnvName)", k, prev, plans[i].name)
			}
			owner[k] = plans[i].name
			env[k] = res.Env[k]
		}
	}

	suite.setProvisioned(handles, env)

	return env
}

func releaser(t *testing.T, name string, res *core.Resource) func() {
	return func() {
		if res.Release == nil {
			return
		}

		ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
		defer cancel()

		if err := res.Release(ctx); err != nil {
			t.Errorf("release %q: %v", name, err)
		}
	}
}
