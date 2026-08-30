# Local Folder sync writes directly from the Mac app, not via a local server subprocess

Local Folder is a `SyncDestination` that copies Photos-library assets straight to disk from the
Mac app's own process, reusing the server's on-disk conventions (date-organized path, identifier
suffix) as plain Swift logic — not by running the existing Go server as a background subprocess
pointed at `localhost` with `STORAGE_PATH` set to the chosen folder.

**Naming note (superseded from this ADR's original wording):** the suffix rule referenced here is
the server's *original* collision-only suffix (`_<backend_id>`, appended only when a destination
path already existed). Designing Local Folder's own naming surfaced that collision-only suffixing
is unsound without a server database (see `docs/plans/20260829-apple-local-folder-sync.md`): a
plain existence check can't distinguish "my own already-synced file" from "a different asset's
file at the same date+filename" without an unconditionally-embedded identifier. Local Folder
therefore always appends `_<SafeID(resourceKey)>`; the companion server plan will adopt that rule
for the server as well. This ADR's subprocess-vs-direct-write decision is unaffected; only the
"collision suffix" phrasing below is corrected.

We considered the subprocess route because it would reuse the server's TUS proxy, BadgerDB state,
and file-organization code as-is, with the Mac app treating "local" the same as "remote." We
rejected it: `FilesNest.entitlements` has `com.apple.security.app-sandbox` enabled, and a plain
spawned child process inherits the parent's sandbox container rather than gaining independent
filesystem access — whether a security-scoped bookmark's access reliably extends to such a child
is an unverified, testable-but-unconfirmed mechanism, not a given. Even if it does work, the
project would take on a permanent build-pipeline cost (cross-compiling and code-signing an embedded
Go binary as part of app notarization, keeping its version in lockstep with the Swift app) to reuse
machinery — TUS resumability, a shared-state database — that exists to solve problems (flaky WAN
transfer, multi-client state authority) a local disk write doesn't have. The part actually worth
reusing (the date-organized path shape and identifier-suffix rule) is small enough to reimplement
directly in Swift without any of that cost.
