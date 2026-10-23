package verity

// Option configures a suite in NewSuite. NewSuite collects every option first and
// then runs its phases in a fixed order, so the order options are passed in does
// not matter.
type Option interface {
	apply(*config)
}

// config is what the options collect, for NewSuite to act on.
type config struct {
	uses     []use
	services []serviceConfig
	envs     []map[string]string
}

type use struct {
	name string
	opts []any
}

type optionFunc func(*config)

func (f optionFunc) apply(c *config) { f(c) }

// Use provisions an isolated resource from the compose backend called name (its
// compose service name), and makes it available through Get. Its provider is
// chosen by the service's verity.type label. opts are provider-specific, such as
// postgres.EnvName.
func Use(name string, opts ...any) Option {
	return optionFunc(func(c *config) {
		c.uses = append(c.uses, use{name: name, opts: opts})
	})
}

// WithService starts the service under test in-process once the Use resources are
// provisioned, wired to their env, and waits for it to be ready. It is stopped
// before the resources are released.
func WithService(fn ServiceFunc, opts ...ServiceOption) Option {
	sc := serviceConfig{fn: fn, readyPath: defaultReadyPath, readyTimeout: defaultReadyTimeout, stopTimeout: defaultStopTimeout}
	for _, o := range opts {
		o(&sc)
	}

	return optionFunc(func(c *config) {
		c.services = append(c.services, sc)
	})
}

// WithEnv adds static config to the service's env, such as a JWT secret. It is
// merged with the env of the Use resources; setting a var that a resource also
// sets, or that another WithEnv sets, fails the suite.
func WithEnv(env map[string]string) Option {
	return optionFunc(func(c *config) {
		c.envs = append(c.envs, env)
	})
}
