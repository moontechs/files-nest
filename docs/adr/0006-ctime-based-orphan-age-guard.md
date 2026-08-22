# Use ctime, not mtime, as the orphan-scan age guard

## Status

Accepted

## Context

`orphans.FilterMinAge` (`server/internal/orphans/filter.go`) drops any
orphan candidate younger than `minAge` (default 3h, `server/main.go`). It
exists as a race guard: `filestore.MoveFile` can write a file into
`organized/` moments before its DB record commits to `complete`, and
without this filter `orphans.Scan` would flag that file as an orphan and
`Apply` would delete a completed upload out from under a still-committing
record. The guard works today because a freshly moved file's `mtime` is
naturally close to "now" — `os.Rename`/`io.Copy` set it at write time.

Files are now given a real, client-supplied `creation_date` as their
on-disk `mtime`/`atime` (via `os.Chtimes`, called from `MoveFile` right
after the rename/copy) so that filesystem tools, backups, and directory
listings show the photo's actual capture date instead of its upload date.
This is the whole point of the change requested — but it directly
contradicts what the age guard assumes: a backed-up photo from 2015
uploaded today gets an `mtime` of 2015, which looks decades older than
`minAge` the instant it lands on disk. The guard would pass it as "safe"
immediately, reopening exactly the race window it was built to close, for
what will be the overwhelming majority of files (backed-up photos are
essentially never dated today).

`mtime` and `atime` are both settable by any process via `os.Chtimes`, so
neither can serve as a race guard once either is driven by client input.
`ctime` (inode/file status-change time) is the one POSIX timestamp no
process can set directly — the kernel stamps it on every metadata change,
including the `Chtimes` call itself — so it still means "when did this
file last get touched here," independent of what timestamp value was
written into it. `ctime` isn't exposed on `os.FileInfo`; reading it
requires a type-asserted `Sys().(*syscall.Stat_t)`, with a different field
name on Linux (`Ctim`) vs. Darwin (`Ctimespec`). The server has no Windows
target (no build tags, no Windows-specific code anywhere in the tree), so
two platform files cover the real deployment/dev matrix.

## Decision

Replace `Candidate.ModTime` with `Candidate.CTime`, sourced from
`syscall.Stat_t` behind a small platform-specific `ctime()` helper
(`_linux.go` / `_darwin.go` build tags) inside the `orphans` package.
`FilterMinAge` compares against `CTime` instead of `ModTime`; its
signature, default `minAge` (3h), and log-and-continue error handling are
unchanged. `Chtimes` failures in `MoveFile` are themselves best-effort
(logged, not fatal to the move) so a bad/unparseable `creation_date` never
blocks a completion.

Existing files already under `organized/` before this change keep
whatever `mtime` they have; there is no backfill pass. Their `ctime` is
unaffected either way, so the guard's correctness for pre-existing files
is untouched.

**Constraint this decision depends on:** the storage root must be a local
POSIX-compliant filesystem. `ctime` semantics (and the `syscall.Stat_t`
shape read via `info.Sys()`) are not guaranteed to behave identically on
network-mounted volumes (NFS, SMB/CIFS, FUSE-backed mounts) — caching
layers on those filesystems can make `ctime` lag or diverge from local
kernel semantics. Today's deployment (`docker-compose.yml`, `driver:
local`) satisfies this, but nothing in code enforces it: pointing the
storage volume at a network mount is an ops-only change that would
silently degrade this guard's safety property. If `ctime` extraction ever
starts failing (`orphans.ctime` returning an error), the affected
candidates are excluded from cleanup entirely rather than incorrectly
deleted — a safe failure mode, but one that fails *silently* (orphan
cleanup quietly stops working for those files) unless the resulting
`Result.Errors` entries are actually watched. Ops guidance: the storage
root must stay a local filesystem, and a sustained non-zero count of
`ctime`-read errors in the `gc-orphans` logs should be treated as a page,
not noise.

## Considered Options

- **Keep mtime, exempt files with a client-supplied `creation_date` from
  the guard some other way** (e.g. a separate "recently moved" marker).
  Rejected: reintroduces exactly the kind of extra state (a marker that
  must itself be written and cleaned up reliably) the in-process,
  stateless guard was designed to avoid, for a problem `ctime` already
  solves for free.
- **Track write time in the DB record instead of on the filesystem**
  (e.g. read `CompletionIntent`/`Upload.CreatedAt` during the scan instead
  of statting the file). Rejected: `orphans.Scan` already builds its
  known-path set from DB records for path matching; folding write-time
  into that lookup would work but costs an extra DB read per candidate
  and complicates the package's stated design of using bare filesystem
  facts (`Path`, one timestamp) for the age check. `ctime` gives the same
  answer as a single, already-available stat field.
- **ctime via syscall.Stat_t** (chosen). No extra state, no extra I/O
  beyond the stat the scan already performs, not spoofable by the same
  `Chtimes` call that now drives `mtime`/`atime`. Cost: two small
  platform-specific files instead of one platform-neutral one, scoped to
  a deployment matrix (Linux + Darwin) the project already has.
