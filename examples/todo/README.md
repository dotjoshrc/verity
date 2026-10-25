# todo example

A small Postgres-backed HTTP service (`todo.go`) with two test suites
(`suites/alpha`, `suites/beta`) that run in parallel against shared Postgres,
Redis, and WireMock containers. Each suite gets its own database clone, Redis
ACL user and key prefix, WireMock URL prefix, and service instance.

Both suites register the same mock path with different title responses, fetch
their own response, store it under the same Redis key suffix, and post the cached
title through the service to Postgres. The service itself uses only Postgres;
the suites use the Redis and WireMock handles directly.

Run it from this directory (needs Docker):

```sh
go run ../../cmd/verity run --go-parallel-packages 2 -- -v
```

To demonstrate automatic backend logs and cleanup after a failed suite method:

```sh
TODO_FAIL=1 go run ../../cmd/verity run -- -v
```

This deliberately fails the alpha suite after emitting `todo failure-log marker`
in Postgres. The failure output includes that marker in the backend logs, and
Verity still tears the stack down.

Under a plain `go test` the suites skip, because they need the stack that
`verity run` starts. Redis isolation requires every key to use `Keyspace.Prefix()`;
WireMock requests must preserve `Server.URL()`'s path prefix. These prevent
accidental collisions, not hostile access to a shared server.
