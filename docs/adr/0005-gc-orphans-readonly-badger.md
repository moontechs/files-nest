# Run orphan cleanup as an in-process goroutine, not a standalone CLI

## Status

Accepted

## Context

`organized/` (the completed-upload storage tree) and the Badger `uploads/`
records can drift apart: `HandleDeleteUpload` (`server/internal/api/handlers.go:818-889`)
removes the on-disk file before marking the record `deleted`, and a failed
`os.Remove` is only logged, not retried — leaving a file on disk whose
record already says `deleted`. Startup recovery (`server/internal/api/recovery.go`)
only reconciles DB-side completion intents against files; nothing scans
storage for files no live record points at.

Badger takes an exclusive directory lock (`flock(LOCK_EX)`) when opened
read-write, which is how the server always opens it
(`server/internal/store/store.go:17`, `badger.Open` with default options).

An earlier version of this ADR proposed a standalone `gc-orphans` CLI
opening the same Badger directory with `WithReadOnly(true)`, reasoning that
Badger's read-only mode would coexist with the server's read-write process
already holding the directory open. That reasoning was wrong, and was never
implemented before the error was caught.

Badger's directory lock (`dir_unix.go:acquireDirectoryLock`) uses `flock`
with `LOCK_NB` (non-blocking) in both modes: `LOCK_EX` for a read-write
open, `LOCK_SH` for a read-only open. `flock` semantics: an exclusive lock
held by one process blocks *any* other lock request from a different
process — shared or exclusive. So a second process opening the same
directory with `WithReadOnly(true)` while the server holds it read-write
does not get shared read access; it fails immediately with `"Cannot acquire
directory lock ... Another process is using this Badger database."`
Confirmed empirically (two processes against the same directory, one
read-write, one read-only, real `dgraph-io/badger/v4` v4.9.2). Badger also
exposes `WithBypassLockGuard`, but its own documentation warns it "can
cause data corruption if multiple badger instances are using the same
directory" — not a viable substitute for correct locking, even read-only,
since a bypassed reader can still race the writer's compaction (which
deletes SSTable files a concurrent scan may be mid-read on).

In short: **no second process can open the server's Badger directory at
all** while the server is running, in any mode. A standalone CLI cannot run
against a live server without either the server closing its handle first
(a maintenance window) or the CLI never opening Badger directly (e.g.
receiving a snapshot over the network) — both meaningfully more complex
than the alternative below.

## Decision

Run the orphan scan and cleanup as a **background goroutine inside the
server process itself** (`server/main.go`), using the server's single,
already-open `*store.Store` handle. It runs immediately at startup and then
on a configurable interval (`GC_ORPHANS_INTERVAL`, default 48h), mirroring
the shape of the existing `runBadgerGC` goroutine (`server/main.go:165-189`).

This sidesteps the locking problem entirely rather than working around it:
there is never a second Badger handle, so there is no lock conflict to
reason about, bypass, or race. It also requires no new binary, no snapshot
transfer mechanism, and no maintenance window — cleanup runs continuously
as part of the server's own lifecycle, which was the actual goal ("routine
cleanup shouldn't require downtime every time it runs").

The tradeoff: cleanup can no longer be triggered ad hoc from a shell
(`gc-orphans --apply --json` outside the server's own schedule) or observed
via CLI stdout — it only runs on the server's internal schedule, and its
output is server log lines. Deletion is also fully autonomous (no
dry-run/manual-approval step), guarded only by a fixed minimum-age filter
(3 hours) on candidates, to avoid racing an in-flight upload's completion.
This is an accepted risk, not a limitation of the chosen architecture.

## Considered Options

- **Standalone CLI with read-only Badger access.** Rejected: factually
  doesn't work, per the empirical finding above — Badger's directory lock
  has no mode that permits a second process to open the directory at all
  while the server holds it open.
- **Standalone CLI, requires the server to be stopped first.** Rejected:
  turns a routine cleanup command into a down-time event every time it's
  run, which discourages running it regularly — the opposite of what a
  garbage-collection command needs. (Same rejection reason as the original
  version of this ADR; still holds.)
- **Standalone CLI receiving a live snapshot over the network** (server
  periodically calls `DB.Backup()` and streams it, or exposes it on demand;
  CLI loads the snapshot into a throwaway local Badger instance and scans
  that). Would work — `Backup`/`Load` are real Badger APIs designed for
  this — but adds a new server endpoint, an HTTP round-trip, and
  snapshot-staleness as a new variable, for no benefit over running the
  scan directly in the process that already holds the live data. Not
  chosen: strictly more moving parts than the in-process goroutine for the
  same outcome.
- **In-process goroutine** (chosen). No second Badger handle, no new
  binary, no snapshot mechanism, no maintenance window. The cost is losing
  ad hoc/on-demand invocation, judged acceptable since the driving
  requirement was "runs regularly without downtime," not "runs on demand."
