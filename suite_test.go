package verity

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/dotjoshrc/verity/internal/core"
)

// helperEnv selects a failure case for TestHelperProcess to run in a child process.
// Fatal paths cannot be tested in-process: a failing subtest fails its parent.
const helperEnv = "VERITY_SUITE_HELPER"

func init() {
	// The registry panics on duplicates, so register once for the whole binary.
	core.RegisterProvider("fake", fakeProvider{})
}

// recorder collects what the fake provider and the suites do, in order. In a
// helper process it also prints each event so the parent can read the order.
type recorder struct {
	mu     sync.Mutex
	events []string
	seen   map[string]core.Request

	expect  int // provisions that must be in flight at once; 0 disables the barrier
	started int
	allIn   chan struct{}
}

var rec = &recorder{}

func (r *recorder) reset(expect int) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.events, r.started, r.expect = nil, 0, expect
	r.allIn = make(chan struct{})
	r.seen = map[string]core.Request{}
}

func (r *recorder) event(s string) {
	r.mu.Lock()
	r.events = append(r.events, s)
	r.mu.Unlock()

	if os.Getenv(helperEnv) != "" {
		fmt.Println("EVENT:" + s)
	}
}

// arrive blocks until `expect` provisions are in flight, which only happens if
// they run concurrently.
func (r *recorder) arrive(req core.Request) error {
	r.mu.Lock()
	r.seen[req.Endpoint.Name] = req
	r.started++
	if r.started == r.expect {
		close(r.allIn)
	}
	expect, allIn := r.expect, r.allIn
	r.mu.Unlock()

	if expect == 0 {
		return nil
	}

	select {
	case <-allIn:
		return nil
	case <-time.After(2 * time.Second):
		return errors.New("provisions did not run concurrently")
	}
}

// eventsExceptProvision drops the provision events, whose order is not defined.
func (r *recorder) eventsExceptProvision() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.DeleteFunc(slices.Clone(r.events), func(e string) bool { return strings.HasPrefix(e, "provision:") })
}

type (
	failProvision  struct{}
	panicProvision struct{}
	nilResource    struct{}
	noRelease      struct{}
	releaseFails   struct{}
	envKey         string
)

type fakeHandle struct{ name string }

type fakeProvider struct{}

func (fakeProvider) Provision(_ context.Context, req core.Request) (*core.Resource, error) {
	name := req.Endpoint.Name
	rec.event("provision:" + name)

	if err := rec.arrive(req); err != nil {
		return nil, err
	}

	key := "FAKE_" + strings.ToUpper(name)
	for _, o := range req.Options {
		switch o := o.(type) {
		case failProvision:
			return nil, errors.New("boom from provider")
		case panicProvision:
			panic("kaboom")
		case nilResource:
			return nil, nil
		case envKey:
			key = string(o)
		}
	}

	res := &core.Resource{Env: map[string]string{key: name}, Handle: &fakeHandle{name}}
	res.Release = func(context.Context) error { rec.event("release:" + name); return nil }

	for _, o := range req.Options {
		switch o.(type) {
		case noRelease:
			res.Release = nil
		case releaseFails:
			res.Release = func(context.Context) error { rec.event("release:" + name); return errors.New("release boom") }
		}
	}

	return res, nil
}

// setDiscovery sets VERITY_DISCOVERY to a document with one backend per entry of
// types (name -> type).
func setDiscovery(t *testing.T, types map[string]string) {
	t.Helper()

	d := core.Discovery{RunID: "run1", Backends: map[string]core.Endpoint{}}
	for name, typ := range types {
		d.Backends[name] = core.Endpoint{Name: name, Type: typ, Host: name}
	}

	s, err := d.Encode()
	require.NoError(t, err)
	t.Setenv(core.DiscoveryEnv, s)
}

type fakeSuite struct {
	Suite
	initID string
}

func (s *fakeSuite) Init() error { s.initID = s.ID(); return nil }

func (s *fakeSuite) TestOne(t *testing.T) { rec.event("test") }

func (s *fakeSuite) AfterSuite(*testing.T) error { rec.event("afterSuite"); return nil }

type fatalSuite struct{ Suite }

func (s *fatalSuite) TestBoom(t *testing.T) { t.Fatal("boom") }

type orderedSuite struct{ Suite }

func (s *orderedSuite) BeforeSuite(*testing.T) error { rec.event("beforeSuite"); return nil }
func (s *orderedSuite) TestOne(t *testing.T)         { rec.event("test") }
func (s *orderedSuite) AfterSuite(*testing.T) error  { rec.event("afterSuite"); return nil }

type badSignatureSuite struct{ Suite }

func (s *badSignatureSuite) TestNoArgs() {}

func (s *fatalSuite) AfterSuite(*testing.T) error { rec.event("afterSuite"); return nil }

type initFailsSuite struct{ Suite }

func (s *initFailsSuite) Init() error { return errors.New("init boom") }

type beforeFailsSuite struct{ Suite }

func (s *beforeFailsSuite) BeforeSuite(*testing.T) error { return errors.New("before boom") }
func (s *beforeFailsSuite) TestNever(t *testing.T)       { rec.event("test") }

type afterFailsSuite struct{ Suite }

func (s *afterFailsSuite) AfterSuite(*testing.T) error {
	rec.event("afterSuite")
	return errors.New("after boom")
}
func (s *afterFailsSuite) TestOne(t *testing.T) { rec.event("test") }

func TestNewSuiteProvisionsConcurrentlyAndCleansUpInReverse(t *testing.T) {
	rec.reset(2) // both fake provisions must be in flight together

	t.Run("suite", func(t *testing.T) {
		setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})

		s := NewSuite[fakeSuite](t, Use("a"), Use("b"))
		RunSuite(s)

		require.NotEmpty(t, s.ID())
		require.Equal(t, s.ID(), s.initID, "Init runs after the ID is set")
		require.NotEmpty(t, s.UniqueName())
		require.Equal(t, map[string]string{"FAKE_A": "a", "FAKE_B": "b"}, s.env)
		require.Equal(t, "a", Get[*fakeHandle](s, "a").name)
		require.Equal(t, "b", Get[*fakeHandle](s, "b").name)

		// ID and run ID were set before the providers ran.
		for _, name := range []string{"a", "b"} {
			require.Equal(t, s.ID(), rec.seen[name].SuiteID)
			require.Equal(t, "run1", rec.seen[name].RunID)
		}
	})

	// AfterSuite first (it may still use the resources), then resources in reverse.
	require.Equal(t, []string{"test", "afterSuite", "release:b", "release:a"}, rec.eventsExceptProvision())
}

type parallelSuite struct{ Suite }

func (s *parallelSuite) TestParallel(t *testing.T) {
	t.Parallel() // runs only after RunSuite has returned
	time.Sleep(20 * time.Millisecond)
	rec.event("parallel test")
}

func (s *parallelSuite) AfterSuite(*testing.T) error { rec.event("afterSuite"); return nil }

func TestNewSuiteWithoutUseNeedsNoDiscovery(t *testing.T) {
	t.Setenv(core.DiscoveryEnv, "")
	_ = os.Unsetenv(core.DiscoveryEnv)

	s := NewSuite[fakeSuite](t)
	require.NotEmpty(t, s.ID())
}

func TestResourceWithoutReleaseIsFine(t *testing.T) {
	rec.reset(0)

	t.Run("suite", func(t *testing.T) {
		setDiscovery(t, map[string]string{"a": "fake"})
		NewSuite[fakeSuite](t, Use("a", noRelease{}))
	})

	require.Empty(t, rec.eventsExceptProvision())
}

func TestBeforeSuiteRunsBeforeTests(t *testing.T) {
	rec.reset(0)

	t.Run("suite", func(t *testing.T) {
		RunSuite(NewSuite[orderedSuite](t))
	})

	require.Equal(t, []string{"beforeSuite", "test", "afterSuite"}, rec.eventsExceptProvision())
}

func TestTestsRunCallsHooksAroundEachCase(t *testing.T) {
	rec.reset(0)
	hook := func(s string) TestHookFunc { return func(*testing.T) { rec.event(s) } }

	t.Run("cases", func(t *testing.T) {
		Tests[int, int]{
			{Name: "one", Request: 1, WantResponse: 2, BeforeFunc: hook("before1"), AfterFunc: hook("after1")},
			{Name: "two", Request: 2, WantResponse: 4},
		}.Run(t, func(t *testing.T, test Test[int, int]) {
			rec.event(t.Name()[strings.LastIndex(t.Name(), "/")+1:])
			require.Equal(t, test.Request*2, test.WantResponse)
		})
	})

	require.Equal(t, []string{"before1", "one", "after1", "two"}, rec.eventsExceptProvision())
}

func TestAfterSuiteWaitsForParallelTests(t *testing.T) {
	rec.reset(0)

	t.Run("suite", func(t *testing.T) {
		setDiscovery(t, map[string]string{"a": "fake"})
		RunSuite(NewSuite[parallelSuite](t, Use("a")))
	})

	require.Equal(t, []string{"parallel test", "afterSuite", "release:a"}, rec.eventsExceptProvision())
}

// failureCase is one scenario run in a child process. output are substrings the
// failure must print; events is the exact order of everything the cleanups ran.
type failureCase struct {
	mode   string
	run    func(t *testing.T)
	output []string
	events []string

	noProvision bool // validation must fail before any provider runs
}

var failureCases = []failureCase{
	{
		mode: "fatal-in-test",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})
			RunSuite(NewSuite[fatalSuite](t, Use("a"), Use("b")))
		},
		output: []string{"boom"},
		events: []string{"afterSuite", "release:b", "release:a"},
	},
	{
		mode: "fatal-test-func-runs-after-func",
		run: func(t *testing.T) {
			Tests[int, int]{{Name: "x", AfterFunc: func(*testing.T) { rec.event("afterFunc") }}}.Run(t, func(t *testing.T, _ Test[int, int]) {
				t.Fatal("boom")
			})
		},
		output: []string{"boom"},
		events: []string{"afterFunc"},
	},
	{
		mode: "provider-fails-others-released",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})
			NewSuite[fakeSuite](t, Use("a"), Use("b", failProvision{}))
		},
		output: []string{`verity.Use("b")`, "boom from provider"},
		events: []string{"release:a"},
	},
	{
		mode: "provider-panics-others-released",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})
			NewSuite[fakeSuite](t, Use("a"), Use("b", panicProvision{}))
		},
		output: []string{`verity.Use("b")`, "provider panicked: kaboom"},
		events: []string{"release:a"},
	},
	{
		mode:   "init-error",
		run:    func(t *testing.T) { NewSuite[initFailsSuite](t) },
		output: []string{"failed to initialize suite", "init boom"},
	},
	{
		mode:   "not-a-suite",
		run:    func(t *testing.T) { NewSuite[struct{}](t) },
		output: []string{"does not implement verity.TestSuite"},
	},
	{
		mode: "before-suite-error",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			RunSuite(NewSuite[beforeFailsSuite](t, Use("a")))
		},
		output: []string{"failed to run before suite", "before boom"},
		events: []string{"release:a"}, // no test ran, no AfterSuite registered
	},
	{
		mode: "after-suite-error",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			RunSuite(NewSuite[afterFailsSuite](t, Use("a")))
		},
		output: []string{"failed to run after suite", "after boom"},
		events: []string{"test", "afterSuite", "release:a"},
	},
	{
		mode: "release-error",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})
			NewSuite[fakeSuite](t, Use("a"), Use("b", releaseFails{}))
		},
		output: []string{`release "b"`, "release boom"},
		events: []string{"release:b", "release:a"}, // a failing release must not stop the others
	},
	{
		mode: "nil-resource",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			NewSuite[fakeSuite](t, Use("a", nilResource{}))
		},
		output: []string{`verity.Use("a")`, "no resource"},
	},
	{
		mode: "missing-discovery",
		run: func(t *testing.T) {
			t.Setenv(core.DiscoveryEnv, "")
			NewSuite[fakeSuite](t, Use("a"))
		},
		output: []string{"verity run"},
	},
	{
		mode: "unknown-backend",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"b": "fake", "a": "fake"})
			NewSuite[fakeSuite](t, Use("a"), Use("zzz"))
		},
		output:      []string{`verity.Use("zzz")`, "no such backend", "available: a, b"},
		noProvision: true,
	},
	{
		mode: "bad-test-signature",
		run: func(t *testing.T) {
			RunSuite(NewSuite[badSignatureSuite](t))
		},
		output: []string{"badSignatureSuite.TestNoArgs", "func(t *testing.T)"},
	},
	{
		mode: "duplicate-use",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			NewSuite[fakeSuite](t, Use("a"), Use("a"))
		},
		output:      []string{`verity.Use("a") is used twice`},
		noProvision: true,
	},
	{
		mode: "unknown-type",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "nope", "b": "fake"})
			NewSuite[fakeSuite](t, Use("b"), Use("a"))
		},
		output:      []string{`no provider for type "nope"`, "providers/nope"},
		noProvision: true,
	},
	{
		mode: "env-conflict",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})
			NewSuite[fakeSuite](t, Use("a", envKey("SAME")), Use("b", envKey("SAME")))
		},
		output: []string{"SAME", `"a"`, `"b"`, "EnvName"},
		events: []string{"release:b", "release:a"}, // already provisioned, so released
	},
	{
		mode: "withenv-conflict-with-resource",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			NewSuite[fakeSuite](t, Use("a"), WithEnv(map[string]string{"FAKE_A": "x"}))
		},
		output: []string{"FAKE_A", "verity.WithEnv"},
		events: []string{"release:a"},
	},
	{
		mode: "withenv-conflict-with-withenv",
		run: func(t *testing.T) {
			NewSuite[fakeSuite](t, WithEnv(map[string]string{"K": "1"}), WithEnv(map[string]string{"K": "2"}))
		},
		output: []string{"K", "verity.WithEnv"},
	},
	{
		mode: "get-missing",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			Get[*fakeHandle](NewSuite[fakeSuite](t, Use("a")), "nope")
		},
		output: []string{`verity.Get("nope")`, "no such resource"},
		events: []string{"release:a"},
	},
	{
		mode: "get-wrong-type",
		run: func(t *testing.T) {
			setDiscovery(t, map[string]string{"a": "fake"})
			Get[string](NewSuite[fakeSuite](t, Use("a")), "a")
		},
		output: []string{`verity.Get("a")`, "*verity.fakeHandle", "not string"},
		events: []string{"release:a"},
	},
}

// TestHelperProcess is the child side of TestFailurePaths. It skips unless asked.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv(helperEnv)
	if mode == "" {
		t.Skip("only runs as a child of TestFailurePaths")
	}

	rec.reset(0)

	for _, c := range failureCases {
		if c.mode == mode {
			c.run(t)
			return
		}
	}

	t.Fatalf("unknown helper mode %q", mode)
}

func TestFailurePaths(t *testing.T) {
	for _, c := range failureCases {
		t.Run(c.mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$", "-test.count=1")
			cmd.Env = append(os.Environ(), helperEnv+"="+c.mode)
			out, err := cmd.CombinedOutput()

			var exitErr *exec.ExitError
			require.ErrorAs(t, err, &exitErr, "the child test must fail:\n%s", out)

			for _, want := range c.output {
				require.Contains(t, string(out), want)
			}

			var events []string
			for line := range strings.Lines(string(out)) {
				if e, ok := strings.CutPrefix(line, "EVENT:"); ok && !strings.HasPrefix(e, "provision:") {
					events = append(events, strings.TrimSpace(e))
				}
			}
			require.Equal(t, c.events, events, "output:\n%s", out)

			if c.noProvision {
				require.NotContains(t, string(out), "EVENT:provision:")
			}
		})
	}
}
