# Verity milestone 2: more backends and a safer run

Status: in progress, 2026-10-24. Shared discovery prerequisite, host-gateway injection, and Redis provider
implemented; remaining steps and spikes are pending. Companion to
`DESIGN.md`, which stays the source of truth for milestone 1 and the overall
design. Rows marked "default" are proposals that nobody has confirmed.

## Summary

Milestone 1 proved the loop with one backend (Postgres) and a happy-path CLI.
Milestone 2 makes it useful for real stacks and harder to leak from:

1. A shared prerequisite: make discovery and the CLI's per-type table carry
   backends that are not Postgres.
2. Redis and a mock server as providers.
3. Failure-log capture, so a failing test shows what the backends said.
4. Leak prevention for a killed CLI (`verity down`, and a watchdog or reaper).
5. Postgres hardening: per-suite roles, then a `CONNECTION LIMIT`.
6. Per-test isolation, mostly by documenting and tightening what `NewSuite` in a
   subtest already does.

Container mode, `verity discover`, remote providers, and non-Go adapters are not
in this milestone (see "Out of scope").

## What the code looks like today (the constraints)

- `core.Endpoint` is `name, type, host, port, user, password, database`. The
  discovery JSON is `version: 1` and `json.Unmarshal` ignores unknown fields, so
  adding optional fields is compatible with older readers.
- `cmd/verity/discovery.go` has one `backendTypes` table (`port`, a `resolve` that
  reads `POSTGRES_*` env, and `fileVars`). `resolve` returns
  `(user, password, database)`, which is Postgres-shaped.
- `publishLoopback` already mutates the loaded compose project before `Up`. It is
  the natural place to inject `extra_hosts`.
- The library never talks to Docker. `NewSuite` gets only `VERITY_DISCOVERY`.
- `NewSuite(t, ...)` provisions against the `t` it is given and mints a fresh
  v7 ID per call, so calling it inside `t.Run` already gives one slice per
  subtest.
- Providers return `Resource{Env, Handle, Release}`. Postgres hands suites the
  admin user's credentials in `DATABASE_URL`.

## Goals and non-goals

**Goals**

- A suite can `verity.Use` a Redis and a mock server beside Postgres, each isolated
  from other suites, with no change to the discovery contract's version.
- A failing test prints enough backend log context to debug without `--keep`.
- A killed CLI leaves a stack that one command removes.
- Isolation weaker than database-level says so, in the provider's docs and its
  error messages, instead of implying more than it gives.

**Non-goals**

- Anything in "Out of scope" below.
- A public provider API (still `internal/core`; unchanged from `DESIGN.md`).

## Decisions for milestone 2

Unconfirmed rows are marked "default". Change them here before implementing.

| Question                                        | Decision                                                                                                                                                                                                                                                                                                                         |
| ----------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Discovery version                               | Stays `1`. New `Endpoint` fields are optional and `omitempty`. Old library with new CLI ignores them. New library with old CLI sees them empty, so a provider must treat empty as "use the default" and fail with a clear message only when a value is required. (default)                                                          |
| New `Endpoint` fields                           | Add `Extra map[string]string` (`json:"extra,omitempty"`), filled from `verity.opt.<key>` labels. No new typed fields until a spike shows one is needed: Redis needs only user and password, and a mock server needs only host and port. (default)                                                                                  |
| Redis isolation level                           | ACL user restricted to a key prefix and a channel prefix, with dangerous and keyless-global commands denied. The level is "moderate", not "strong": `SCAN` still lists foreign key names (spike 1). A server without the needed ACL support (below Redis 7) is an error naming Redis 7, not a silent downgrade to prefix-only; a `redis.PrefixOnly()` opt-in can be added if someone needs it. |
| Redis credentials                               | Label-only (`verity.password`, `verity.user`). The official image takes `--requirepass` on its command line, not from env, so there is nothing to read from the service environment. A missing password with a password-protected server fails with a message naming the label. (default)                                          |
| Mock server product                             | WireMock, namespaced by URL path prefix `/verity/<suite>/`; the service must take its upstream base URL from an env var. **This is the user's call.** WireMock is the default because it has an admin API, a healthcheck, and a stub-by-metadata cleanup. (default)                                                                |
| Mock-server contract on the service             | Same shape as the Redis prefix: it only isolates if the service reads the base URL (and prefix) the provider hands it. Documented as a requirement on the service under test, next to the in-process runner requirements.                                                                                                            |
| Callbacks on Linux (`host-gateway`)             | The CLI injects `extra_hosts: ["host.docker.internal:host-gateway"]` into every service that does not already define that host. Decided by the host-gateway spike; closes `DESIGN.md` open question 4. (default)                                                                                                                  |
| Failure-log capture transport                   | The library shells out to `docker compose -p <project> logs --since <test start> --no-color --tail N` in a `t.Cleanup` when the test failed. It never imports `docker/compose`. If `docker` is not on PATH, it logs one line saying so and carries on. The project name comes from a new optional discovery field, `project`. (default) |
| Failure-log noise                               | Logs are stack-wide for the window, so parallel suites show up in each other's output. Accepted and documented. Filtering by suite is out of scope. (default)                                                                                                                                                                     |
| Hard-kill cleanup                               | Two layers: `verity down [--all]` removes projects labelled as verity's (always), and a watchdog is added only if the spike favours it over a reaper image (below). (default)                                                                                                                                                      |
| Per-test isolation                              | No new provisioning engine. Document and test calling `NewSuite` inside a subtest, and add `verity.PerTest()` only if the spike shows `RunSuite` cannot give a fresh suite value per method safely. (default)                                                                                                                      |
| Per-suite Postgres roles before `CONNECTION LIMIT` | Roles come first, because superusers are exempt from a database's `CONNECTION LIMIT` (to be confirmed by the spike). Until a suite connects as a non-superuser, the limit does nothing. (default)                                                                                                                              |
| Container mode                                  | Moved to milestone 3. It is the largest item by far and its open question (macOS bind-mount and VM CPU cost) is unmeasured. **This is the user's call.** (default)                                                                                                                                                               |

## Step 0: Spikes

Throwaway code. Results are written into this section, and each step below says
which spike gates it.

1. **Redis ACL isolation (gates step 3).** On Redis 7, create a user with
   `ACL SETUSER <u> on ><pw> ~<prefix>:* &<prefix>:* +@all -@dangerous -@admin
   -flushall -flushdb -select`. Find out which of these still reach another
   suite's data or the server: `FLUSHALL`, `FLUSHDB`, `SELECT`, `CONFIG`, `KEYS *`,
   `SCAN`, `PUBLISH` to another prefix, `EVAL` with computed keys, `INFO keyspace`.
   Record the final deny list and whether `go-redis` works as that user. If
   key-pattern ACLs cannot stop `KEYS` and `SCAN` from listing foreign keys, record
   that: the level is then "no accidental collision", and the docs must say so.
2. **Mock server (gates step 4).** Start WireMock in compose with a healthcheck.
   Register stubs under `/verity/<id>/...` through the admin API, tag them with
   metadata, and delete by metadata. Confirm two suites cannot match each other's
   stubs by path and that a callback to `host.docker.internal` works. If the
   user picks another product, repeat for it.
3. **`host-gateway` injection (gates step 4 and closes open question 4).**
   Append `extra_hosts` to every loaded service in `publishLoopback`'s sibling
   and confirm a container reaches a host listener on Linux. Check it is harmless
   where Docker Desktop already defines the name, and that a user-set entry is
   kept.
4. **Postgres superuser and `CONNECTION LIMIT` (gates step 7).** On Postgres 17,
   set a limit of 2 on a database and open 5 connections as the superuser, then as
   a plain role. Expect the superuser to be exempt. Also create a role, `GRANT`
   it `CONNECT` on a clone, and check it can read the cloned schema and data
   (clones keep the source's owner, so a new role may need grants).
5. **Hard-kill cleanup (gates step 6).** Compare two ways to remove a project when
   the CLI is `SIGKILL`ed:
   - A watchdog: the CLI re-execs itself detached (`setsid`) with the project name
     and its own pid, polls the pid, and runs `Down` when it disappears. No image,
     no socket mount, same-host only (remote daemons are already out of scope).
     Find out whether `kill -9` of the process group or a closed terminal kills it
     too.
   - A reaper container (ryuk-style): needs a published image, the Docker socket
     path on Linux and Docker Desktop, and a connection back to the CLI.
   Pick one, or neither if `verity down` is enough.
6. **Failure logs (gates step 5).** Confirm `docker compose -p <project> logs
   --since` works from the test process with only the project name, on Linux and
   macOS, and how it behaves when `docker` is absent or the project is gone.
7. **Per-test isolation (gates step 8).** Run `NewSuite` inside a subtest 50 times
   in parallel against one Postgres and measure clone time and connection count.
   Check whether a fresh suite value per `Test*` method is achievable in `RunSuite`
   without surprising shared-field semantics.

Acceptance: each bullet is confirmed or replaced with a working alternative, and
the decisions table above is updated for any default that changes.

**Results.**

- **Spike 1, Redis ACL (2026-10-23, Redis 7.4.11).** Confirmed with the deny list
  `-@dangerous -select -randomkey -dbsize -script|flush -script|kill -function
  -pubsub` on top of `+@all`, a `~prefix*` key pattern and a `&prefix*` channel
  pattern (after `reset resetchannels`). Foreign keys cannot be read, written,
  deleted, copied, renamed, watched or measured (`GET`, `SET`, `DEL`, `COPY`,
  `RENAME`, `MSET`, `MEMORY USAGE`, `OBJECT`, `EVAL` with a foreign key); foreign
  channels cannot be published to or subscribed to. `FLUSHALL`, `FLUSHDB`,
  `KEYS`, `CONFIG`, `INFO`, `ACL`, `CLIENT LIST|KILL|PAUSE`, `SAVE`, `MONITOR`,
  `SORT` are refused by `-@dangerous`. **Not covered by `-@dangerous`, so denied
  explicitly:** `SELECT`, `RANDOMKEY` (returns a foreign key name), `DBSIZE`,
  `SCRIPT FLUSH|KILL`, `FUNCTION *`, `PUBSUB *`. **Cannot be denied:** `SCAN`
  lists every key name on the server, so isolation is "moderate": values and
  channels are isolated, key names are not hidden. Denying `SCAN` would break
  services that scan their own keys, so it stays allowed and is documented.
  `-@connection` is not denied: go-redis needs `HELLO` and `CLIENT SETINFO`.
  go-redis connects and works as the restricted user.
- **Spike 1 follow-up: server versions.** Redis 6.0 has no channel ACLs
  (`resetchannels` is a syntax error) and 6.2 rejects negative subcommand rules
  (`-script|flush`: "Unknown command or category name"). Redis 7 is the minimum.

## Step 1: Shared prerequisite

- Add `Extra map[string]string` to `core.Endpoint` and an optional `Project string`
  to `core.Discovery`. Both `omitempty`; `DiscoveryVersion` stays `1`.
- Fill `Extra` from `verity.opt.<key>` labels in `buildDiscovery`.
- Make the CLI's per-type table generic: `resolve` returns a `core.Endpoint`
  fragment (user, password, database, extra) instead of three strings, so a new
  type does not have to look like Postgres. Keep `port` and `fileVars`.
- Fix two stale comments found on the way: `DiscoveryEnv` says "each test
  container" and `Endpoint` says "from inside the compose network"; both are host
  addresses since the host-mode switch.

Acceptance: table-driven tests cover `Extra` from labels, an unknown label key
being ignored, and the cross-version cases (JSON without `extra` and `project`
decodes; JSON with them decodes in a reader that ignores them). Postgres
discovery output is unchanged for an existing fixture.

## Step 2: `host-gateway` injection

Implemented (2026-10-24). Unit tests cover additions, existing mappings, overrides,
and repeat application. The Docker-gated callback test passes on Linux; Docker
Desktop compatibility remains unverified. Services sharing another container’s
network namespace are skipped because Docker rejects `extra_hosts` there; the
network-owning service receives the mapping.

- In the CLI, before `Up`, add `host.docker.internal:host-gateway` to the
  `extra_hosts` of every service that does not define that host.

Acceptance: a unit test on a loaded project covers added, kept, and
user-overridden. A Docker-gated test shows a container reaches a host listener on
Linux. `DESIGN.md` open question 4 is marked closed.

## Step 3: Redis provider

Gated by spike 1. Built; see the results above.

- `verity/providers/redis`, registered from `init` like Postgres. Type `redis`,
  default port 6379, credentials from labels (see decisions). Needs Redis 7 or
  later.
- `Provision`: create an ACL user named from the run and suite, restricted to the
  prefix `verity:<run>_<hash>:`, with the spike's deny list. Return a `*Keyspace`
  handle (`Client()` is a `go-redis` client as that user, plus `URL()` and
  `Prefix()`) and `REDIS_URL` and `REDIS_KEY_PREFIX` in `Resource.Env`. Options
  mirror Postgres: `redis.EnvName`, `redis.PrefixEnvName`.
- `Release`: delete the ACL user (which closes its connections), then `UNLINK`
  every key under the prefix with a scan on the admin connection (`compose down`
  still reclaims everything; this frees memory mid-run).
- Admin client: one per endpoint, capped at 4 connections, shared per process, as
  in Postgres.
- A server below Redis 7 is an error that says so. There is no prefix-only
  fallback; add a `redis.PrefixOnly()` opt-in if someone needs one.

Acceptance (against a fixture Redis from compose): eight concurrent `Provision`
calls return distinct prefixes and users; a key written by one suite is not
readable, writable or deletable by another; the spike's denied commands fail;
`Release` removes the user and every key, and is safe to repeat; a missing or
wrong admin password names `verity.password`; a Redis 6 server gives an error that
names Redis 7.

## Step 4: Mock-server provider

Gated by spikes 2 and 3, and by the user's choice of product.

- `verity/providers/wiremock` (name follows the product). Type `wiremock`, default
  port 8080.
- `Provision`: reserve a path prefix for the suite, return a `Server` handle with
  `URL()` (the base URL, prefix included) and helpers to register stubs through
  the admin API, tagged with the suite's metadata. Env: `MOCK_BASE_URL` (default
  name, configurable with `EnvName`).
- `Release`: delete the suite's stubs by metadata.
- Callbacks: `Env.Addr()` already returns `host.docker.internal:<port>`; the
  provider documents using it as the stub's callback target.

Acceptance: two suites register the same path under different prefixes and only
see their own stubs; a callback from the mock to a service running in a suite
works on Linux with step 2; `Release` leaves no stubs.

## Step 5: Failure-log capture

Gated by spike 6.

- In `RunSuite`, for each `Test*` method, record the start time and register a
  `t.Cleanup` that, when `t.Failed()`, runs `docker compose -p <project> logs
  --since <start> --no-color --tail 200` and calls `t.Log` with the result.
- The project name is the `project` discovery field. Absent field (an older CLI)
  or absent `docker`: one log line and no capture. Never fail a test over it.
- Output is capped so a chatty backend cannot flood the report.

Acceptance: a deliberately failing test shows backend logs from its window; a
passing test shows none; with `docker` removed from PATH the test still reports
its own failure and one line saying capture was skipped.

## Step 6: Hard-kill cleanup

Gated by spike 5.

- `verity down [--all] [project]`: remove compose projects whose name starts
  with `verity-`. No argument and no `--all` lists them and exits non-zero
  without removing anything; naming a project, or `--all`, removes it.
- Add the watchdog or the reaper, whichever spike 5 chose. If neither beats
  `verity down`, record that and skip.

Acceptance: `SIGKILL` the CLI mid-run, then `verity down --all` leaves no
containers or networks from the run. If the watchdog is built, `SIGKILL` alone
leaves none after a few seconds. `verity down` with no argument removes nothing.

## Step 7: Postgres per-suite roles, then `CONNECTION LIMIT`

Gated by spike 4. The order matters: the limit is meaningless for superusers.

- Roles: each suite gets a login role that owns its clone and can connect to
  nothing else, with no `CREATEDB`, `CREATEROLE`, or superuser. `DATABASE_URL`
  carries that role. This also removes the weakness `DESIGN.md` documents (a
  suite's DSN can reach any other suite's database). `Release` drops the role.
- `postgres.ConnLimit(n)` option: `CREATE DATABASE ... CONNECTION LIMIT n`, off by
  default so the service's own pool is not capped unexpectedly. Documented with
  the pool-times-suites arithmetic from `DESIGN.md`.
- Compatibility: suites that need superuser (server-wide objects) are already
  unsupported; the error from a denied statement is left as Postgres's.

Acceptance: a suite's role cannot connect to another suite's clone or to the
source; a test that creates a server-level object gets a permission error; with
`ConnLimit(2)`, the third connection as the suite's role is refused; `Release`
drops the role and the database.

## Step 8: Per-test isolation

Gated by spike 7.

- Add a test and a README paragraph showing `NewSuite` inside `t.Run`, with the
  measured cost from the spike.
- Add `verity.PerTest()` only if the spike shows `RunSuite` can hand each `Test*`
  method a fresh suite value safely; otherwise document the in-subtest pattern and
  stop. Do not add a second provisioning path.

Acceptance: a test with two subtests, each calling `NewSuite`, sees disjoint data;
clone and connection numbers from the spike are recorded in the docs.

## Definition of done

From `examples/`, one example suite uses Postgres, Redis, and the mock server
together, runs two packages in parallel, passes under `verity run`, shows backend
logs for a deliberately failing test, and leaves no containers or networks behind.
After a `SIGKILL` of the CLI, one `verity down --all` returns the daemon to a clean
state. `go build`, `go vet`, `go test ./...`, and the Docker-gated tests pass.

## Out of scope for milestone 2

- Container mode, in all forms. It moves to milestone 3 behind a measurement spike
  for open question 2.
- `verity discover`. Ports are ephemeral and only known after `Up`, so a bare
  `discover` can only print the static half. `verity run --keep` already prints the
  real value for IDE use. Revisit with a clearer use case.
- Remote provisioning, non-Go adapters, Kubernetes, remote Docker daemons, a public
  provider API, and per-suite filtering of failure logs.

## Risks and open questions

1. **Redis ACLs may not isolate as much as the design table says** ("moderate").
   Spike 1 decides, and the docs must state the result.
2. **Prefix and base-URL isolation depend on the service.** A service that ignores
   `REDIS_KEY_PREFIX` or `MOCK_BASE_URL` is not isolated. Verity cannot detect it.
3. **Per-suite roles change what `DATABASE_URL` can do.** Existing users whose
   suites rely on admin rights (extensions, `CREATE ROLE`) will see failures. This
   is the intended hardening, but it is a behaviour change to announce.
4. **`docker compose logs` from the test process** assumes the Docker CLI and
   compose plugin are on PATH there. Best effort only.
5. **Watchdog survival.** A process-group kill or a closed terminal may take the
   watchdog with the CLI. Spike 5 measures it.
