# Adopt go-gremlins for mutation testing on the server

## Status

Accepted

## Context

`server`'s unit tests (`internal/api`, `internal/store`, `internal/filestore`,
`internal/uploadbackend`) all pass today, but passing tests don't prove the
assertions are strong — a test can exercise a code path without actually
verifying its behavior, and coverage tooling can't tell the difference.
Mutation testing closes that gap: it perturbs the production code (flips a
condition, changes a boundary, drops a return value) and checks whether the
existing test suite notices. A mutant that survives means some behavior is
unverified.

`go-gremlins` (github.com/go-gremlins/gremlins) is the mutation-testing tool
being adopted for this. There's no CI pipeline in this repo yet (no
`.github/workflows`), and `e2e/` tests run against a live Docker Compose
stack rather than in-process — mutating that tier would be slow and flaky
without the payoff of exercising individual unit-level assertions.

## Decision

- Scope mutation testing to `internal/...` (`api`, `store`, `filestore`,
  `uploadbackend`) via a `.gremlins.yaml` config at the `server/` root;
  `e2e/` and the root package are excluded.
- Add a `mutation-test` target to `server/Makefile`, matching the existing
  `test`/`lint`/`e2e-test` naming convention. The target requires `gremlins`
  to already be on `PATH` and errors with an install hint if it's missing —
  no auto-install, no pinned `tools.go` dependency, keeping the target a
  thin wrapper rather than a tool-management layer.
- Surviving mutants found on the first run are fixed by strengthening the
  corresponding unit tests, not by loosening gremlins' config or ignoring
  them — the goal of adopting the tool is to drive the suite's assertions
  to actually match its coverage.
- Local-only for now: the target isn't wired into CI, since there is no CI
  in this repo to wire it into yet. Follow-up if/when a CI pipeline is
  introduced.
- `.gremlins.yaml` pins `workers: 2`, `test-cpu: 1`, and a raised
  `timeout-coefficient` rather than leaving them at gremlins' defaults.
  Gremlins defaults `workers` to the host's CPU count with no `test-cpu`
  cap, so each worker's `go test` can itself use every CPU — on a 10-core
  machine that's 10 workers × unrestricted CPU each, and under that
  contention wall-clock test timeouts fire nondeterministically. Measured
  directly: the same run against `internal/api` reported anywhere from 0 to
  46 to 124 "timed out" mutants across successive runs with no code changes,
  and two mutants in `internal/filestore` intermittently reported as LIVED
  under contention were reliably KILLED once concurrency was capped. Without
  pinning these, `threshold-efficacy: 100` would flap on unrelated machine
  load rather than reflecting actual test strength.

## Considered Options

- **Auto-install gremlins via `go run .../gremlins@latest` inside the make
  target.** Rejected: adds a network dependency and version-drift risk to a
  target meant to run repeatedly during local test-strengthening; requiring
  a one-time manual install is simpler and matches how `golangci-lint` is
  already handled in this Makefile (also assumed pre-installed).
- **Pin gremlins via a `tools.go` + `go.mod` tool dependency.** Rejected as
  more ceremony than a single local dev tool warrants right now; revisit if
  CI adoption makes version reproducibility across machines matter.
- **Set a mutation-score threshold instead of fixing all survivors.**
  Rejected for this first pass — with only four packages and a clean slate,
  chasing every survivor now is cheaper than deciding later which gaps are
  "acceptable."
