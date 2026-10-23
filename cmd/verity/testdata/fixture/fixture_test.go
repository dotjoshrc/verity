package fixture

import (
	"net"
	"os"
	"os/signal"
	"strconv"
	"testing"
	"time"

	"github.com/dotjoshrc/verity/internal/core"
)

func discover(t *testing.T) core.Endpoint {
	d, err := core.DecodeDiscovery(os.Getenv(core.DiscoveryEnv))
	if err != nil {
		t.Fatal(err)
	}
	return d.Backends["db"]
}

func TestReachesDB(t *testing.T) {
	e := discover(t)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(e.Host, strconv.Itoa(e.Port)), 3*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
}

func TestFails(t *testing.T) {
	if os.Getenv("FIXTURE_FAIL") != "" {
		t.Fatal("asked to fail")
	}
}

func TestSlow(t *testing.T) {
	if os.Getenv("FIXTURE_IGNORE_SIGINT") != "" {
		signal.Ignore(os.Interrupt)
	}
	if d := os.Getenv("FIXTURE_SLEEP"); d != "" {
		dur, _ := time.ParseDuration(d)
		time.Sleep(dur)
	}
}
