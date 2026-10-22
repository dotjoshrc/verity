package verity

// Option configures a suite in NewSuite. NewSuite collects every option first and
// then runs its phases in a fixed order, so the order options are passed in does
// not matter.
type Option interface {
	apply(*config)
}

// config is what the options collect, for NewSuite to act on.
type config struct {
	uses []use
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
