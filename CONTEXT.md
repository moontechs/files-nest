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
The on-disk location of a completed upload. A Local Folder sync destination uses `YYYY/MM/DD/<filename>_<id>` directly below the chosen folder, always appending `<id>` (a stable hash of the resource key), with no `incoming/` or `organized/` staging. The server currently uses its collision-only `organized/YYYY/MM/DD/<filename>` naming beneath `$STORAGE_PATH` and stores that as `OrganizedPath` when a record reaches `complete`; the companion server plan will adopt the always-suffixed local-folder rule.
_Avoid_: File path, storage path

**Orphan file**:
A file under `organized/` whose path is not the `OrganizedPath` of any upload record currently in `complete` status — either no record ever referenced it, or the referencing record has since moved to `deleted` while the on-disk delete failed. Target of the server's background `gc-orphans` scan cycle.
_Avoid_: Stale file, leftover file

**Completion intent**:
A durable record written before an in-progress upload's file is moved into `organized/`, used by startup recovery to detect and finish/roll back a move interrupted by a crash. Reconciles DB-side state against files — the opposite direction from orphan detection, which reconciles files against DB-side state.
_Avoid_: Move record, pending move
