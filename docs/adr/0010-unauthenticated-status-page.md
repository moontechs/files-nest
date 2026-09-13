# Serve the status page unauthenticated

`GET /` (see CONTEXT.md: Status page) is served without HTTP Basic Auth, unlike every other route except `/health`.

We considered gating it behind Basic Auth like the upload API, so it would leak nothing to an unauthenticated caller. We rejected that: the whole point of the status page is to make the server's state legible from a browser without credentials — on app-store platforms (Umbrel, ZimaOS, TrueNAS, Unraid) a fresh install has no credentials configured yet, and the operator needs to see *something* (is it running, what version, is auth even on) before they can act. This also isn't a new exposure model: the server already runs fully unauthenticated on every route, including uploads, when `BACKUP_USER`/`BACKUP_PASS` are unset (`server/main.go`, warn-not-block) — the status page surfaces that state instead of hiding it behind a log line nobody without SSH access will ever read.

The page carries no upload data (no file listing, no counts, no per-file browsing) — only server identity (version, address) and its own auth state. Credentials themselves are never generated or displayed by the server; the operator sets `BACKUP_USER`/`BACKUP_PASS` manually.
