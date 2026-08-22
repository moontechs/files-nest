# FilesNest

Self-hosted backup: a macOS/iOS client streams Photos-library assets to a Go
server over resumable (TUS) HTTP uploads. The server is the sync-state
authority; the client keeps no local database.

Two independent codebases, each with its own toolchain and conventions:

- `apple/` — Swift client (macOS app + shared `FilesNestCore` package). See `apple/CLAUDE.md`.
- `server/` — Go backend. See `server/CLAUDE.md`.

Read `apple/CLAUDE.md` or `server/CLAUDE.md` depending on which side you're
touching — most tasks only need one.

## Shared context

- `PRODUCT.md` — product purpose, users, positioning, principles.
- `CONTEXT.md` — domain vocabulary shared by client and server (upload
  lifecycle terms, etc.). Read before naming anything upload/sync related.
- `docs/architecture.md` — end-to-end system architecture (client + server).
- `docs/adr/` — accepted architectural decisions, numbered, one per file.
- `docs/design/` — dated design notes for individual features/components.
- `docs/distribution/pre-tester-verification.md` — manual verification checklist.

When making a non-trivial design decision, check `docs/adr/` first for an
existing decision before re-deciding it.
