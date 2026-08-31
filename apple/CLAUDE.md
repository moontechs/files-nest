# apple/ — Swift client

macOS menu-bar app that backs up a Mac Photos library to a FilesNest server.
Business logic lives in a shared Swift package, `FilesNestCore`, consumed by
the macOS app target (`ios/` exists as a platform target but the shipped
product scope is macOS — see `../PRODUCT.md`).

## Layout

- `FilesNestCore/` — SwiftPM package (`swift-tools-version: 6.0`, macOS 13+ /
  iOS 17+). All sync engine, upload, credential-store, and server-client
  logic. Has its own `Sources/FilesNestCore` and `Tests/FilesNestCoreTests`.
- `macos/FilesNest/` — the Xcode project for the macOS app
  (`FilesNest.xcodeproj`). Views, app model, settings — thin layer over
  `FilesNestCore`.
- `ios/` — iOS target.

## Commands

From `apple/FilesNestCore/`:

- `swift build` — build the package.
- `swift test` — run `FilesNestCoreTests`.

The macOS app (`macos/FilesNest/FilesNest.xcodeproj`) has two test targets,
same as `swift test` but app-hosted, so they need `xcodebuild` (no CLI
wrapper here):

- `FilesNestTests` — app-hosted unit tests (e.g. `SettingsModelTests`).
- `FilesNestUITests` — XCUITest end-to-end tests: builds and launches the
  real `.app` and drives it via the accessibility tree (`XCUIApplication`).
  Currently just the default Xcode-generated stubs; add real coverage here
  the way `SettingsModelTests` covers the model layer.

Run either with:

```bash
xcodebuild -project macos/FilesNest/FilesNest.xcodeproj -scheme FilesNest test -destination 'platform=macOS'
```

There's no CI for the macOS app — everything above runs locally only. See
"Local code signing" below before running this for the first time.

### Linux

`FilesNestCore` also builds and tests on Linux (`swift test` under the
official `swift` Docker images). Apple-only functionality — Keychain,
memory-footprint diagnostics, and security-scoped bookmarks (Local Folder
sync has no Linux equivalent, see `LocalFolderStore.swift`) — is
compile-time guarded out via `#if canImport(Security)` /
`#if canImport(Darwin)`, so those tests simply don't build there rather than
failing. `ServerClient.sendOnce` explicitly checks `Task.checkCancellation()`
before each request rather than relying on `URLSession` to observe task
cancellation — Darwin's URLSession does that cooperatively, Linux's
`swift-corelibs-foundation` doesn't reliably.

Run with `swift test --no-parallel`: Swift Testing's default parallel
scheduler has been observed to hang deterministically partway through the
suite in Docker on this platform (0% CPU, every test passes individually or
serially) — an environment/scheduler issue, not a test bug. `--no-parallel`
runs the full 266-test Linux-compatible subset in a few seconds.

### Local code signing

`xcodebuild test` code-signs the app and its test bundles. Without a
configured team, it falls back to ad-hoc "Sign to Run Locally" signing, whose
identity is derived from the binary's own hash — it changes on every
rebuild, so the app looks like a new, unrecognized binary to Keychain each
time and you get an "Allow access to Keychain" prompt on every single test
run.

Fix once per machine: copy `macos/FilesNest/Local.xcconfig.example` to
`Local.xcconfig` (gitignored) and set `DEVELOPMENT_TEAM` to your Apple ID's
team (Xcode → Settings → Accounts; a free personal team works, no paid
Developer Program needed — the team ID is the `OU=` field of the "Apple
Development" certificate, findable via `security find-certificate -c "Apple
Development" -p | openssl x509 -noout -subject`; if none exists yet, create
one from Accounts → Manage Certificates… → "+" → Apple Development). This
gives the app a stable signing identity, so the Keychain grant persists
across rebuilds instead of re-prompting.

Building the very first time after that still needs one interactive
approval — codesign asks the OS for permission to use the new certificate's
private key, and that dialog can only be answered from a real GUI session
(an agent's non-interactive shell can't see it and fails closed with
`errSecInternalComponent`). Build or test once from Xcode itself (⌘B or ⌘U)
or run `xcodebuild` from Terminal.app directly, click Allow/Always Allow, and
every run after that — including from an agent — is prompt-free.

### Pre-commit hook

The repo ships a plain shell pre-commit hook at `.githooks/pre-commit` (no
new dependency) that runs `swift test` for `FilesNestCore` when staged
changes touch `apple/`, and blocks the commit on any test failure. The same
hook also gates `server/` changes with `golangci-lint` — see
`server/README.md`. Install it once per clone, from the repo root:

```bash
git config core.hooksPath .githooks
```

It does not run `xcodebuild test` or the UI tests — those need the
interactive Keychain approval described above, which a hook can't get. Run
those yourself before a PR when you've touched the macOS app or UI-visible
behavior.

## Conventions

- Put engine/sync/upload logic in `FilesNestCore`, not in the app target —
  it's the shared, testable layer and the iOS target depends on it too.
- Credentials go through `KeychainStore`/`CredentialStore` — never
  `UserDefaults` or plists (`../PRODUCT.md` calls this out explicitly).
- Streams data without app-owned temp files or whole-file buffering — one
  resource processed at a time. Keep that invariant when touching
  `AssetUploader`/`AssetDataSource`/`CallbackStreamReader`.
- Destination implementations are intentionally concrete and parallel: keep
  server and local-folder coordinators behind `LiveSyncEngine`'s existing
  closures rather than introducing a protocol solely to unify the two call
  sites.
- Domain vocabulary is defined in `../CONTEXT.md` — use those exact terms.
- New component/feature design notes go in `../docs/design/` as a new
  `YYYYMMDD-name.md` file, matching the existing ones — check there first for
  prior design decisions before re-deciding something (e.g. resume/persisted
  plan, upload concurrency).
- For a Settings window launched from the menu-bar app, use `SettingsPresenter`:
  it temporarily activates the app as `.regular` before opening Settings and restores
  `.accessory` when the Settings view closes.
