# Verity

_A Go framework for integration tests against real, live services._

## What is Verity?

Verity is a Go testing framework designed to simplify the process of writing and
running integration tests. It provides a structured way to define test suites,
manage test dependencies, and execute tests in a consistent manner.

It is opinionated. Integration tests should run against real infrastructure,
each suite isolated from the others, with a clear setup and teardown. Verity
enforces that, so the tests you write are reliable and easy to maintain.

This isn't meant to compete with existing testing frameworks like `testing` or
`testify`. Instead, this was created to provide more specific functionality for
integration testing.

## Problems it solves

- **Test Organization**: Verity allows you to organize your tests into suites,
  making it easier to manage and maintain large test codebases.
- **Consistent Test Execution**: Verity offers a consistent way to run tests,
  regardless of the underlying testing framework being used (e.g., `testing`,
  `testify`, etc.), allowing you to focus on writing tests rather than worrying
  about the mechanics of test execution.

## Principles

- **Live Services**: Infrastructure your system owns and depends on, like
  databases and caches, should be tested against real, running instances instead
  of mocks, so tests exercise the same behavior your system will see in
  production.
- **Mockable Boundaries**: Third-party services outside your system's boundary
  should be easy to fake or stub out, so your tests aren't coupled to the
  availability or behavior of something you don't control.
- **Isolation**: Each test suite should run in its own isolated namespace — its
  own database, schema, or key prefix — so suites never share state with one
  another.
- **Parallel by Default**: Because suites are isolated from each other, they
  should be safe to run in parallel. Tests within a single suite still run
  sequentially, sharing that suite's setup and teardown.
- **Clear Lifecycle**: Each test suite should have a clear setup and teardown
  process to ensure that the environment is properly prepared for testing and
  cleaned up afterward.

## How it works

Verity runs many suites in parallel against **one shared, ephemeral compose
stack**. Each suite gets its own slice of every backend (today, a Postgres
database cloned from the schema your compose stack already created) and its own
in-process instance of your service, wired to that slice.

1. Label the backends in your compose file. Compose stays the only source of
   truth; there is no second config.

   ```yaml
   services:
     db:
       image: postgres:17
       environment:
         POSTGRES_PASSWORD: postgres
         POSTGRES_DB: app # the database holding your schema; suites clone it
       labels:
         verity.type: postgres
       healthcheck:
         test: pg_isready -U postgres
   ```

2. `verity run` brings the stack up with ephemeral ports bound to `127.0.0.1`,
   then runs `go test` on the host against it. Each run has its own compose
   project and ports, so concurrent runs cannot collide.
3. In a suite, `verity.Use` provisions a slice of a backend, and
   `verity.WithService` starts your service on it. Your service needs an
   injectable entrypoint that reads config through the `getenv` it is given, not
   `os.Getenv`, and stops when its context is cancelled.

> **Status:** early. The library (`Use`, `Get`, `WithService`, the Postgres
> provider) and `verity run` both work. See [`examples/todo`](examples/todo) for
> a runnable project. Suites that call `Use` need `VERITY_DISCOVERY`, which only
> `verity run` sets, so they fail under a plain `go test` with a message saying
> so.

## CLI

### Install

**Homebrew**

```sh
brew install dotjoshrc/tap/verity@latest
```

**Go**

```sh
go install github.com/dotjoshrc/verity/cmd/verity@latest
```

**Mise**

```sh
mise use go:github.com/dotjoshrc/verity/cmd/verity@latest
```

### Usage

```sh
verity run [flags] [-- go test args]
```

`verity run` reads `./docker-compose.yaml`, brings it up under a per-run project
name, waits for the services to be healthy, runs `go test ./... -count=1` with
`VERITY_DISCOVERY` set, then tears the stack down. Anything after `--` goes to
`go test` unchanged, and the exit code is `go test`'s.

| Flag                          | Default                | Meaning                                    |
| ----------------------------- | ---------------------- | ------------------------------------------ |
| `--docker-compose-file`       | `./docker-compose.yaml` | Compose file                               |
| `--docker-compose-wait-timeout` | `2m`                 | How long to wait for services to be ready  |
| `--go-dir`                    | `./...`                | Packages to test                           |
| `--go-parallel-packages`      | `2`                    | `go test -p`                               |
| `--go-parallel-tests`         | `1`                    | `go test -parallel`                        |
| `--go-tags`                   | none                   | Build tags                                 |
| `--go-run`                    | none                   | `go test -run`                             |
| `--go-timeout`                | Go's default (10m)     | `go test -timeout`                         |
| `--go-verbose`                | off                    | `go test -v`                               |
| `--go-fail-fast`              | on                     | `go test -failfast`                        |
| `--keep`                      | off                    | Leave the stack up and print how to reach it |
| `--debug` (before `run`)      | off                    | Debug logging                              |

Flags can also come from `VERITY_<FLAG>` env vars (dots become underscores, e.g.
`VERITY_GO_DIR`) or a `.verity.yaml` (also `.yml`, `verity.yaml`, `verity.yml`)
in the working directory, with keys like `go.dir`, `go.parallel_packages` and
`docker_compose.file`.

#### Compose labels

| Label                                            | Meaning                                                       |
| ------------------------------------------------ | ------------------------------------------------------------- |
| `verity.type`                                    | Backend type. Only `postgres` today. Required to be managed.   |
| `verity.port`                                    | Internal port, if not the type's default (5432 for Postgres).  |
| `verity.user`, `verity.password`, `verity.database` | Override the admin credentials read from the service's env. |

A one-shot service (for example, a migration job) can be labelled
`verity.wait: "true"`. Verity waits for it to exit 0 before running tests, and
fails the run if it exits otherwise.

Backends need a healthcheck, and `*_FILE` env vars (e.g. `POSTGRES_PASSWORD_FILE`)
are not supported; use the labels instead.

## Module

```sh
go get github.com/dotjoshrc/verity@latest
```

### Basic Usage

```go
type MySuite struct {
    verity.Suite
}

func (s *MySuite) TestSomething(t *testing.T) {
    t.Log("Testing something")
}

func TestMySuite(t *testing.T) {
    suite := verity.NewSuite[MySuite](t,
        verity.Use("db"), // compose service name; its verity.type label selects the provider
        verity.WithService(func(ctx context.Context, env verity.Env) error {
            // Read config through env.Getenv and serve on env.Listener.
            return myservice.Run(ctx, env.Getenv, env.Listener())
        }),
    )

    db := verity.Get[*postgres.Database](suite, "db") // this suite's own database
    _ = db.DB()

    verity.RunSuite(suite)
}
```

`WithService` waits for `GET /healthz` to return 200; use `verity.ReadyPath` to
change the path. `postgres.EnvName` sets the env var that carries the database
URL (default `DATABASE_URL`). Everything is released when the test ends, the
service first and then the resources.
