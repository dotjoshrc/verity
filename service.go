package verity

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"
)

const (
	defaultReadyPath    = "/healthz"
	defaultReadyTimeout = 30 * time.Second
	defaultStopTimeout  = 30 * time.Second
)

// ServiceFunc runs the service under test in-process. It must block until ctx is
// cancelled and then return; nil, context.Canceled and http.ErrServerClosed all
// count as a clean stop, any other error fails the test. It must read config
// through env.Getenv, never os.Getenv, and serve on env.Listener.
type ServiceFunc func(ctx context.Context, env Env) error

// Env is what a ServiceFunc is started with.
type Env struct {
	env  map[string]string
	ln   net.Listener
	addr string
}

// Getenv returns the merged env of every resource from verity.Use. It does not
// fall back to the process environment, which holds the discovery credentials.
func (e Env) Getenv(key string) string { return e.env[key] }

// Listener is pre-bound on all interfaces with a random port. verity closes it
// after the service returns.
func (e Env) Listener() net.Listener { return e.ln }

// Addr is the host.docker.internal:port a backend container reaches the service
// at, for callbacks. Use it instead of 127.0.0.1. On Linux the calling compose
// service needs extra_hosts: ["host.docker.internal:host-gateway"].
func (e Env) Addr() string { return e.addr }

type serviceConfig struct {
	fn           ServiceFunc
	readyPath    string
	readyTimeout time.Duration
	stopTimeout  time.Duration
}

// ServiceOption tunes WithService.
type ServiceOption func(*serviceConfig)

// ReadyPath sets the health path polled until it returns HTTP 200. Default /healthz.
func ReadyPath(path string) ServiceOption {
	return func(c *serviceConfig) { c.readyPath = path }
}

// startService runs sc.fn on a pre-bound listener and returns once it is ready. It
// registers its cleanup first, so a failed start still stops the service. Cleanup
// cancels the context itself: t.Context() is already cancelled when cleanups run,
// which would stop the service under AfterSuite.
func startService(t *testing.T, env map[string]string, sc serviceConfig) {
	t.Helper()

	ln, err := net.Listen("tcp", ":0")
	if err != nil {
		t.Fatalf("service: listen: %v", err)
	}

	_, port, _ := net.SplitHostPort(ln.Addr().String())

	ctx, cancel := context.WithCancel(context.WithoutCancel(t.Context()))
	exited := make(chan struct{})
	var runErr error // read only after exited is closed

	go func() {
		defer close(exited)
		defer func() { // a panic here would end the binary before any cleanup runs
			if r := recover(); r != nil {
				runErr = fmt.Errorf("service panicked: %v", r)
			}
		}()
		runErr = sc.fn(ctx, Env{env: env, ln: ln, addr: net.JoinHostPort("host.docker.internal", port)})
	}()

	t.Cleanup(func() {
		cancel()

		select {
		case <-exited:
			if runErr != nil && !errors.Is(runErr, context.Canceled) && !errors.Is(runErr, http.ErrServerClosed) {
				t.Errorf("service: %v", runErr)
			}
		case <-time.After(sc.stopTimeout):
			t.Errorf("service did not return within %s of its context being cancelled", sc.stopTimeout)
		}

		ln.Close() // usually already closed by the service
	})

	waitReady(t, "http://127.0.0.1:"+port+sc.readyPath, sc.readyTimeout, exited, &runErr)
}

// waitReady polls url until it returns 200, failing early if the service exits.
func waitReady(t *testing.T, url string, timeout time.Duration, exited <-chan struct{}, runErr *error) {
	t.Helper()

	// No keep-alive: idle connections would outlive the service as goroutines.
	client := &http.Client{Timeout: time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()

	var last error
	for {
		resp, err := client.Get(url)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
			err = fmt.Errorf("status %d", resp.StatusCode)
		}
		last = err

		select {
		case <-exited:
			t.Fatalf("service exited before it was ready: %v", *runErr)
		case <-deadline.C:
			t.Fatalf("service not ready: GET %s did not return 200 within %s (last: %v)", url, timeout, last)
		case <-tick.C:
		}
	}
}
