# todo example

A small HTTP service (`todo.go`) with two test suites (`suites/alpha`,
`suites/beta`) that run in parallel against one shared Postgres. Each suite gets
its own clone of the `todo` database and its own instance of the service.

Run it from this directory (needs Docker):

```sh
go run ../../cmd/verity run -- -v
```

Under a plain `go test` the suites skip, because they need the stack that
`verity run` starts.
