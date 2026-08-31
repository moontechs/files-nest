# Always append an identifier suffix to organized filenames

## Status

Accepted

## Context

`filestore.Mover.PlanDestination` originally appended a `_<backendID>` suffix
to an organized file's name only when a collision was detected — an
`os.Stat` on the computed `organized/YYYY/MM/DD/<filename>` path found
something already there. The suffix source was `upload.BackendID`, tusd's
per-upload-attempt ID, which `store.Store.ReRegister` reassigns on a
`backend_lost` recovery while `upload.ID` stays fixed for the record's
lifetime.

Designing Local Folder sync (a companion `apple/` project; see
`docs/adr/0008-local-folder-sync-writes-directly-no-subprocess.md` and
`docs/plans/20260829-apple-local-folder-sync.md`) surfaced that
collision-only suffixing is unsound when there is no server database behind
the destination: a plain existence check cannot distinguish "my own
already-synced file" from "a different asset's file at the same
date+filename" without an unconditionally-embedded per-asset identifier.
Local Folder must always suffix for its own correctness, so the server is
changed to match — both destinations now produce identical filenames for
the same asset.

## Decision

`PlanDestination` computes `organized/YYYY/MM/DD/<stem>_<id><ext>`
unconditionally — never only on collision — with `id` sourced from
`upload.ID` (`SafeID(resourceKey)`, a SHA-256/base64url hash of
`<localIdentifier>#<kind>`, stable for a record's lifetime) instead of the
mutable `upload.BackendID`. The collision-detection `os.Stat` branch is
removed from the naming decision entirely, so the destination is fully
deterministic from `(creationDate, filename, id)` alone with no dependence
on current disk state.

Consequences of replacing the disk-state-dependent branch:

- **No collision fallback between two live records.** Dedup at the store
  layer (`PutUploadIfAbsent` keys records by `LocalIdentifier` and returns
  the existing record on a repeat) means no two live `store.Upload` rows
  can share an `id` — the hash's uniqueness alone is not the guarantee.
  The `moveMu` lock separately forecloses the TOCTOU window between
  planning and the actual move.
- **A foreign file at the computed path is an accepted, visible risk.**
  Removing the stat-based collision logic also removes the only signal
  that previously caught a stray file at the target path (manual
  intervention in `organized/`, inconsistent disk state, a bug elsewhere).
  The move now silently overwrites such a file; a single `os.Stat` is
  kept purely as a detection/logging safety net that WARN-logs the
  unexpected pre-existing file before the move proceeds — visibility, not
  a behavior change, and not a hot-loop cost.
- **Recomputation became fully deterministic.** The `moveCompletedFile`
  retry-safety reasoning that justified reusing a persisted
  `CompletionIntent`'s destination verbatim was specific to the old
  mechanism (recomputing could `os.Stat` the already-moved file, treat it
  as a collision, and suffix with the *current* `backendID`, which may
  differ from the first attempt's after a `ReRegister`, producing a path
  different from where the file actually lives). With an unconditional,
  `backendID`-independent suffix this divergence cannot occur; intent
  reuse is retained as a deliberate simplicity/consistency choice, not a
  correctness requirement. `CompletionIntent.BackendID` is retained only as
  debugging/audit context and for best-effort tusd sidecar cleanup during
  crash recovery.
- **Filename length stays bounded.** The `_<id>` suffix (43 chars) plus
  extension must survive intact for the file to remain identifiable and
  correctly typed, so `suffixFilename` truncates only the stem when the
  full `<stem>_<id><ext>` would exceed the 255-byte per-component limit
  (`NAME_MAX`, the bound already used by `SanitizeFilename`).
- **Dead code was removed.** With suffixing unconditional, the
  `(m *Mover) MoveFile` method's `Deduplicated` result (computed by
  comparing against the un-suffixed plain path) became always-true and
  meaningless; the method, `MoveResult`, and the plain-path
  `OrganizedPath` builder had no callers outside this package's tests and
  were deleted (repo-wide grep confirmed). The package-level
  `MoveFile(src, dst, creationDate)` used by startup recovery is
  unaffected — recovery moves to an already-planned destination persisted
  in the `CompletionIntent`.

## Consequences

**Prospectively only — no migration.** Files already organized before this
change ships keep their current names (no suffix, or `_<backendID>`)
forever: no rename pass over existing deployments. Old and new naming
schemes coexist indefinitely on already-deployed servers; only files
organized after this ships get `_<upload.ID>`. `internal/orphans/` (the
gc-orphans scan) reconciles `organized/` files against the `OrganizedPath`
field already stored on each record rather than recomputing expected
paths, so it is unaffected by the suffix rule changing.

Implemented in `docs/plans/20260829-server-unify-organized-filename-suffix.md`
(Tasks 1-4: `filestore` always-append, dead-code removal, `handlers.go`
call-site switch to `upload.ID`, e2e assertions).