package verity

import (
	"os"
	"reflect"
	"strings"
	"testing"
	"uuid"

	"github.com/dotjoshrc/verity/internal/core"
)

// TestSuite is the interface that all test suites must implement. It provides a
// method to access the testing.T instance associated with the suite, as well as
// a method to set the testing.T instance. The testing.T instance is used to run
// tests and log output within the suite.
type TestSuite interface {
	T() *testing.T

	setT(t *testing.T)

	setID(id string)

	ID() string

	setUniqueName(name string)

	UniqueName() string

	// setProvisioned stores what NewSuite provisioned: handles by backend name,
	// and the merged env of every resource.
	setProvisioned(handles map[string]any, env map[string]string)

	handle(name string) (any, bool)
}

// BeforeSuite is an optional interface that test suites can implement to
// perform setup before the suite is run. If a suite implements BeforeSuite, the
// BeforeSuite method will be called by RunSuite before any tests are run.
type BeforeSuite interface {
	BeforeSuite(t *testing.T) error
}

// AfterSuite is an optional interface that test suites can implement to perform
// cleanup after the suite is run. If a suite implements AfterSuite, the
// AfterSuite method will be called by RunSuite after all tests have been run.
type AfterSuite interface {
	AfterSuite(t *testing.T) error
}

// InitSuite is an optional interface that test suites can implement to perform
// initialization before the suite is run. If a suite implements InitSuite, the
// Init method will be called by NewSuite to initialize the suite.
type InitSuite interface {
	Init() error
}

// Suite provides a base implementation of TestSuite that can be embedded in
// test suites.
//
// Example usage:
//
//	type MyTestSuite struct {
//		verity.Suite
//	}
type Suite struct {
	t *testing.T

	id string

	uniqueName string

	handles map[string]any

	env map[string]string
}

// T returns the testing.T instance associated with the suite.
func (s *Suite) T() *testing.T {
	return s.t
}

// setT sets the testing.T instance for the suite. This is called by NewSuite to
// initialize the suite with the testing.T instance.
func (s *Suite) setT(t *testing.T) {
	s.t = t
}

// setID sets the ID for the suite.
func (s *Suite) setID(id string) {
	s.id = id
}

// ID returns the ID for the suite.
func (s *Suite) ID() string {
	return s.id
}

// setUniqueName sets a unique name for the suite.
func (s *Suite) setUniqueName(name string) {
	s.uniqueName = name
}

// UniqueName returns the unique name for the suite.
func (s *Suite) UniqueName() string {
	return s.uniqueName
}

func (s *Suite) setProvisioned(handles map[string]any, env map[string]string) {
	s.handles = handles
	s.env = env
}

func (s *Suite) handle(name string) (any, bool) {
	h, ok := s.handles[name]
	return h, ok
}

// RunSuite runs the test suite by invoking all methods that start with "Test".
// If the suite implements BeforeSuite, it will call the BeforeSuite method
// before running the tests. If the suite implements AfterSuite, it will call
// the AfterSuite method after running the tests, through t.Cleanup so that it
// also runs when a test fails fatally, and before the suite's resources are
// released.
func RunSuite(suite TestSuite) {
	if beforeSuite, ok := suite.(BeforeSuite); ok {
		err := beforeSuite.BeforeSuite(suite.T())
		if err != nil {
			suite.T().Fatalf("failed to run before suite: %v", err)
		}
	}

	if afterSuite, ok := suite.(AfterSuite); ok {
		suite.T().Cleanup(func() {
			if err := afterSuite.AfterSuite(suite.T()); err != nil {
				suite.T().Errorf("failed to run after suite: %v", err)
			}
		})
	}

	suiteType := reflect.TypeOf(suite)

	// Check every signature first so a bad method fails the suite with a clear
	// message instead of a reflect panic part way through.
	for method := range suiteType.Methods() {
		if strings.HasPrefix(method.Name, "Test") && !isTestMethod(method) {
			suite.T().Fatalf("%s.%s must have the signature func(t *testing.T)", suiteType.Elem().Name(), method.Name)
		}
	}

	for method := range suiteType.Methods() {
		name := method.Name

		if strings.HasPrefix(name, "Test") {
			suite.T().Run(name, func(t *testing.T) {
				method.Func.Call([]reflect.Value{reflect.ValueOf(suite), reflect.ValueOf(t)})
			})
		}
	}
}

func isTestMethod(m reflect.Method) bool {
	return m.Type.NumIn() == 2 && m.Type.NumOut() == 0 && m.Type.In(1) == reflect.TypeFor[*testing.T]()
}

// NewSuite creates a new instance of the test suite and initializes it with the
// testing.T instance. The suite must implement the TestSuite interface, and it
// will be initialized with the testing.T instance so that it can be used in the
// suite's methods. If the suite does not implement TestSuite, NewSuite will
// cause the test to fail. If the suite implements InitSuite, NewSuite will call
// the Init method to initialize the suite.
//
// NewSuite must run under `verity run`, which tells it what backends exist. It
// provisions every verity.Use resource concurrently, and releases them with
// t.Cleanup. The order is: set the ID and unique name, call Init, read discovery,
// provision, then merge the resources' env.
//
// Example usage:
//
//	type MyTestSuite struct {
//		verity.Suite
//	}
//
//	func TestExampleSuite(t *testing.T) {
//		suite := verity.NewSuite[MyTestSuite](t, verity.Use("db"))
//		verity.RunSuite(suite)
//	}
func NewSuite[TSuite any](t *testing.T, opts ...Option) *TSuite {
	t.Helper()

	var s TSuite

	suite, ok := any(&s).(TestSuite)
	if !ok {
		t.Fatalf("suite does not implement verity.TestSuite: %T", s)
	}

	suite.setT(t)
	suite.setID(uuid.NewV7().String())
	suite.setUniqueName(getCaller(t)) // must be called here: it reads the caller's frame

	if initSuite, ok := suite.(InitSuite); ok {
		err := initSuite.Init()
		if err != nil {
			t.Fatalf("failed to initialize suite: %v", err)
		}
	}

	var cfg config
	for _, opt := range opts {
		opt.apply(&cfg)
	}

	// Discovery is only required when a suite asks for a backend, so suites
	// without Use still run under a plain `go test`.
	if len(cfg.uses) > 0 {
		disc, err := core.DecodeDiscovery(os.Getenv(core.DiscoveryEnv))
		if err != nil {
			t.Fatal(err)
		}

		provisionUses(t, suite, disc, cfg.uses)
	}

	return &s
}
