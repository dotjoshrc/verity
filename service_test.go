package verity

import (
	"context"
	"errors"
	"net"
	"net/http"
	"runtime"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// toyService is a minimal HTTP service that answers health with the given handler
// and records when it stops.
func toyService(health http.HandlerFunc, started func(Env)) ServiceFunc {
	return func(ctx context.Context, env Env) error {
		if started != nil {
			started(env)
		}

		mux := http.NewServeMux()
		mux.HandleFunc("/healthz", health)
		srv := &http.Server{Handler: mux}
		down := make(chan struct{})
		go func() { defer close(down); <-ctx.Done(); _ = srv.Shutdown(context.Background()) }()

		err := srv.Serve(env.Listener())
		<-down // return only once Shutdown has finished, so nothing outlives the service
		rec.event("service:stop")
		return err // http.ErrServerClosed after Shutdown is a clean stop
	}
}

func healthy(w http.ResponseWriter, _ *http.Request) {}

func TestServiceGetsResourceEnvAndStopsBeforeResourcesRelease(t *testing.T) {
	rec.reset(0)

	var gotEnv Env
	t.Run("suite", func(t *testing.T) {
		setDiscovery(t, map[string]string{"a": "fake", "b": "fake"})

		RunSuite(NewSuite[fakeSuite](t, Use("a"), Use("b"),
			WithService(toyService(healthy, func(e Env) { gotEnv = e }))))
	})

	require.Equal(t, "a", gotEnv.Getenv("FAKE_A"))
	require.Equal(t, "b", gotEnv.Getenv("FAKE_B"))
	require.Empty(t, gotEnv.Getenv("VERITY_DISCOVERY"), "no process env fallback")

	host, port, err := net.SplitHostPort(gotEnv.Addr())
	require.NoError(t, err)
	require.Equal(t, "host.docker.internal", host)
	_, lnPort, _ := net.SplitHostPort(gotEnv.Listener().Addr().String())
	require.Equal(t, lnPort, port)

	// AfterSuite may still use the service, then it stops, then the resources go.
	require.Equal(t, []string{"test", "afterSuite", "service:stop", "release:b", "release:a"}, rec.eventsExceptProvision())
}

func TestReadyPathAndNoLeakedGoroutines(t *testing.T) {
	rec.reset(0)
	before := runtime.NumGoroutine()

	t.Run("suite", func(t *testing.T) {
		NewSuite[fakeSuite](t, WithService(toyService(healthy, nil), ReadyPath("/healthz")))
	})

	// A plain loop: require.Eventually runs its condition in goroutines of its own.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	require.LessOrEqual(t, runtime.NumGoroutine(), before, "goroutines leaked:\n%s", allStacks())
	require.Equal(t, []string{"service:stop"}, rec.eventsExceptProvision())
}

func allStacks() string {
	buf := make([]byte, 1<<16)
	return string(buf[:runtime.Stack(buf, true)])
}

func init() {
	quick := func(c *serviceConfig) { c.readyTimeout = 300 * time.Millisecond }
	unhealthy := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }

	failureCases = append(failureCases,
		failureCase{
			mode: "service-ready-timeout",
			run: func(t *testing.T) {
				setDiscovery(t, map[string]string{"a": "fake"})
				NewSuite[fakeSuite](t, Use("a"), WithService(toyService(unhealthy, nil), quick))
			},
			output: []string{"service not ready", "/healthz", "status 503"},
			events: []string{"service:stop", "release:a"},
		},
		failureCase{
			mode: "service-exits-before-ready",
			run: func(t *testing.T) {
				setDiscovery(t, map[string]string{"a": "fake"})
				NewSuite[fakeSuite](t, Use("a"), WithService(func(context.Context, Env) error { return errors.New("config boom") }))
			},
			output: []string{"exited before it was ready", "config boom"},
			events: []string{"release:a"},
		},
		failureCase{
			mode: "service-panics",
			run: func(t *testing.T) {
				NewSuite[fakeSuite](t, WithService(func(context.Context, Env) error { panic("kaboom") }))
			},
			output: []string{"exited before it was ready", "service panicked: kaboom"},
		},
		failureCase{
			mode: "service-error-on-stop",
			run: func(t *testing.T) {
				NewSuite[fakeSuite](t, WithService(func(ctx context.Context, env Env) error {
					go func() { // ready, then fail when asked to stop
						for ctx.Err() == nil {
							c, err := env.Listener().Accept()
							if err != nil {
								return
							}
							_, _ = c.Write([]byte("HTTP/1.0 200 OK\r\n\r\n"))
							_ = c.Close()
						}
					}()
					<-ctx.Done()
					return errors.New("unclean stop")
				}))
			},
			output: []string{"service: unclean stop"},
		},
		failureCase{
			mode: "service-twice",
			run: func(t *testing.T) {
				setDiscovery(t, map[string]string{"a": "fake"})
				NewSuite[fakeSuite](t, Use("a"), WithService(toyService(healthy, nil)), WithService(toyService(healthy, nil)))
			},
			output:      []string{"WithService is used twice"},
			noProvision: true,
		},
		failureCase{
			mode: "service-nil-func",
			run: func(t *testing.T) {
				setDiscovery(t, map[string]string{"a": "fake"})
				NewSuite[fakeSuite](t, Use("a"), WithService(nil))
			},
			output:      []string{"WithService needs a function"},
			noProvision: true,
		},
	)
}
