# Verity

_A Go framework for integration tests against real, live services._

## What is Verity?

Verity is a Go testing framework designed to simplify the process of writing and
running integration tests. It provides a structured way to define test suites,
manage test dependencies, and execute tests in a consistent manner.

I have strong opinions about how integration tests should be designed and run. I
want to create a framework that enforces these opinions and makes it easy for
developers to write integration tests that are maintainable, reliable, and easy
to understand.

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

<!--TODO: Implement this-->

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

## Module

```sh
go get github.com/dotjoshrc/verity@latest
```

### Basic Usage

```go
import "github.com/dotjoshrc/verity" 

type MySuite struct {
    verity.Suite
}

func (s *MySuite) TestSomething(t *testing.T) {
  t.Log("Testing something")
}

func TestMySuite(t *testing.T) {
  suite := verity.NewSuite[MySuite](t)
  verity.RunSuite(suite)
}
```
