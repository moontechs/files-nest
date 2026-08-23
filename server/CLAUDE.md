# server/ — Go backend

Single Go binary, embedded tusd (TUS 1.0) for resumable uploads, BadgerDB for
metadata. See `../docs/architecture.md` for the full picture and
`README.md` in this directory for the component diagram.

## Layout

- `main.go` — entrypoint/wiring.
- `internal/api` — HTTP handlers.
- `internal/uploadbackend` — embedded tusd adapter.
- `internal/store` — BadgerDB-backed metadata store.
- `internal/filestore` — on-disk organized-file tree.
- `internal/orphans` — background `gc-orphans` scan.
- `e2e/` — end-to-end tests, run against a live Docker Compose stack (build
  tag `e2e`, excluded from `make test`).

## Commands

All via `make` (run from `server/`):

- `make build` — static binary, CGO disabled, to `bin/server`.
- `make test` — unit/integration tests (`go test ./... -v -count=1`).
- `make lint` — golangci-lint, strict config, zero-tolerance. Run before
  considering a change done.
- `make lint-fix` — auto-format + auto-fixable lint issues.
- `make e2e` — full e2e run: build stack → wait for `/health` → test → tear
  down, with diagnostics dumped on failure.
- `make mutation-test` — go-gremlins over `internal/...` (requires
  `gremlins` on PATH).

## Conventions

- Domain vocabulary (concurrent upload, organized path, orphan file,
  completion intent) is defined in `../CONTEXT.md` — use those exact terms,
  the avoid-lists there are deliberate.
- New architectural decisions go in `../docs/adr/` as a new numbered file,
  not inline comments — check there first before re-deciding something.
- Use the logging policy from ADR-0007: INFO for lifecycle events, DEBUG for
  happy-path/per-request noise, and WARN/ERROR for genuine problems. Set
  `LOG_LEVEL=info|debug|trace` to control verbosity.
- Follow the golangci-lint config in this directory (`lint` target is
  zero-tolerance, not advisory).
