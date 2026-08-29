# FilesNest

A backup server that receives files from client devices via a resumable (TUS-protocol) HTTP upload API, and organizes them into a date-based tree on local disk.

## Language

**Sync destination**:
The one active place FilesNest is configured to send backups. Current choices are
the FilesNest server and Local Folder; local-folder sync is future work and is not
ready to receive backups.
_Avoid_: Target (ambiguous with an upload destination URL)

**Concurrent Upload**:
A `PATCH /uploads/{id}/data` request actively streaming bytes to the server right now. Only active byte-streaming counts — an upload record sitting in `uploading` status with no open connection (paused, waiting for the client to resume) is not concurrent; it holds no server resources.
_Avoid_: "in-progress upload", "active upload" (both could be misread as including paused uploads)

**Organized path**:
The on-disk location of a completed upload, date-organized as `YYYY/MM/DD/<filename>_<id>` (the `<id>` suffix, a stable per-resource identifier, is always appended — not just on filename collision). On the server this sits under `organized/` within `$STORAGE_PATH` and is stored on the upload record as `OrganizedPath`, set only when a record reaches `complete` status. On a Local Folder sync destination, the same `YYYY/MM/DD/<filename>_<id>` shape sits directly under the chosen folder — there is no server-side `organized/` segment or upload record to attach it to, since there is no staging step (`incoming/`) to separate it from. Both destinations compute `<id>` identically (a hash of the resource key), so the same asset produces the same filename regardless of destination. Planned in `docs/plans/20260829-server-unify-organized-filename-suffix.md` and `docs/plans/20260829-apple-local-folder-sync.md`; not yet implemented as of this entry.
_Avoid_: File path, storage path

**Orphan file**:
A file under `organized/` whose path is not the `OrganizedPath` of any upload record currently in `complete` status — either no record ever referenced it, or the referencing record has since moved to `deleted` while the on-disk delete failed. Target of the server's background `gc-orphans` scan cycle.
_Avoid_: Stale file, leftover file

**Completion intent**:
A durable record written before an in-progress upload's file is moved into `organized/`, used by startup recovery to detect and finish/roll back a move interrupted by a crash. Reconciles DB-side state against files — the opposite direction from orphan detection, which reconciles files against DB-side state.
_Avoid_: Move record, pending move
