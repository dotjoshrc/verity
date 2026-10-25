# Ryuk cleanup spike

2026-10-24. Result: Ryuk is a viable fallback for killed Verity runs on the tested
Linux daemon. The subsequent CLI integration follows the lifecycle below. Docker Desktop
remains untested.

## Environment and method

- Docker Engine 29.7.2, Linux/arm64, local `/var/run/docker.sock`.
- Image `testcontainers/ryuk:0.14.0`, pulled successfully; registry digest
  `sha256:7c1a8a9a47c780ed0f983770a662f80deb115d95cce3e2daa3d12115b8cd28f0`.
- One reaper per experiment, automatic container removal, socket bind mount,
  ephemeral port published only on `127.0.0.1`. No privileged flag was needed
  on this daemon.
- `RYUK_CONNECTION_TIMEOUT=15s`, `RYUK_RECONNECTION_TIMEOUT=2s` for the spike.
- A throwaway Python harness at `/tmp/verity-ryuk-spike.py` registered an exact
  Compose project label, then created a BusyBox Compose stack with one container,
  one network, one named volume, and one anonymous volume. A separate project’s
  container, network, and volume served as controls.
- Assertions checked that resources survived while the connection was held,
  all four owned resources disappeared after disconnection, the reaper removed
  itself, and the control resources survived. Cleanup was scoped to resources
  created by the harness.

This tested a small connection-holder process standing in for the CLI, not an
integrated `verity run`. Process-group testing killed the holder’s process group;
Ryuk ran independently in Docker.

## Observations

| Trigger                                   | Result                                                                               | Cleanup time after final disconnect |
| ----------------------------------------- | ------------------------------------------------------------------------------------ | ----------------------------------- |
| Close TCP connection                      | Owned resources removed; reaper exited; controls retained                            | 2.77s                               |
| `SIGKILL` connection-holder process       | Same                                                                                 | 2.48s                               |
| `SIGKILL` connection-holder process group | Same                                                                                 | 2.82s                               |
| Reconnect within grace period             | Resources survived beyond the original grace deadline; final disconnect cleaned them | 2.48s                               |

Times are individual observations, not performance guarantees. The harness exited
successfully and removed its control resources. No spike containers, networks,
or volumes remained after verification. The pulled images were retained.

## Protocol and upstream sources

The pinned release is [Ryuk 0.14.0](https://github.com/testcontainers/moby-ryuk/releases/tag/0.14.0).
Its [configuration documentation](https://github.com/testcontainers/moby-ryuk/blob/0.14.0/README.md)
describes the socket, port, and timeout settings. The default initial connection
timeout is 60 seconds and the default reconnection grace is 10 seconds.

Start a reaper along these lines (replace the example project name before use):

```sh
docker run --rm -d \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 127.0.0.1::8080 \
  -e RYUK_RECONNECTION_TIMEOUT=2s \
  testcontainers/ryuk:0.14.0
```

Connect to its published TCP port, send this newline-terminated filter, and wait
for `ACK\n`:

```text
label=com.docker.compose.project=verity-EXACT_RUN_NAME
```

Keep the connection open until normal teardown completes. The newline/filter
handling and cleanup lifecycle are implemented in
[Ryuk’s reaper source](https://github.com/testcontainers/moby-ryuk/blob/0.14.0/reaper.go).
Registrations accumulate; invalid filter parsing can also receive an ACK, so the
client must construct validated filters rather than accepting arbitrary input.

[Testcontainers-Go’s reaper integration](https://github.com/testcontainers/testcontainers-go/blob/main/reaper.go)
provides a reference for socket binding and automatic removal. Its
[Docker host/socket discovery](https://github.com/testcontainers/testcontainers-go/blob/main/internal/core/docker_host.go)
also shows why Desktop requires a daemon-side socket path: the CLI’s host socket
path is not necessarily the correct bind-mount source. These Go references track
upstream `main`; the tested Ryuk image is pinned above.

## Verity integration

1. Start one pinned reaper per run and obtain its connection/ACK before `Up`.
2. Register only the generated, exact Compose project label. Never register a
   broad `verity-*` cleanup filter.
3. Hold the connection through test execution and normal `Down`. Closing it
   afterward lets Ryuk exit even when normal cleanup already removed the stack.
4. Skip launching the reaper for `--keep`.
5. Keep `verity down` as recovery if Ryuk itself fails.

This adds an image pull, a TCP connection, and a container with Docker control
through the mounted socket. It does not stop orphaned host Go test processes or
capture logs after the CLI has died.

The CLI fails startup if Ryuk cannot start or acknowledge registration. It
removes the reaper immediately after successful normal teardown, and leaves it
to reap after a failed teardown. Integrated tests cover normal exit, failures,
`SIGKILL` of the CLI and its process group, and skipping Ryuk for `--keep`.
Docker Desktop socket/port behavior still needs hardware verification. Rootless Docker, remote
daemons, Docker restart, reaper failure, and network partitions were not tested.
The spike’s two-second grace is not a proposed production default.
