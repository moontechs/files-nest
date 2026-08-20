# FilesNest

A backup server that receives files from client devices via a resumable (TUS-protocol) HTTP upload API, and organizes them into a date-based tree on local disk.

## Language

**Concurrent Upload**:
A `PATCH /uploads/{id}/data` request actively streaming bytes to the server right now. Only active byte-streaming counts — an upload record sitting in `uploading` status with no open connection (paused, waiting for the client to resume) is not concurrent; it holds no server resources.
_Avoid_: "in-progress upload", "active upload" (both could be misread as including paused uploads)
