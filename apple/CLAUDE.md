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

The macOS app (`macos/FilesNest/FilesNest.xcodeproj`) builds and tests via
Xcode or `xcodebuild`; there's no CLI wrapper here, use `xcodebuild -project
FilesNest.xcodeproj -scheme FilesNest test` if you need a non-interactive run.

## Conventions

- Put engine/sync/upload logic in `FilesNestCore`, not in the app target —
  it's the shared, testable layer and the iOS target depends on it too.
- Credentials go through `KeychainStore`/`CredentialStore` — never
  `UserDefaults` or plists (`../PRODUCT.md` calls this out explicitly).
- Streams data without app-owned temp files or whole-file buffering — one
  resource processed at a time. Keep that invariant when touching
  `AssetUploader`/`AssetDataSource`/`CallbackStreamReader`.
- Domain vocabulary is defined in `../CONTEXT.md` — use those exact terms.
- New component/feature design notes go in `../docs/design/` as a new
  `YYYYMMDD-name.md` file, matching the existing ones — check there first for
  prior design decisions before re-deciding something (e.g. resume/persisted
  plan, upload concurrency).
- For menu-bar-only macOS apps, a standalone `Settings {}` scene can be opened
  through a view's `openSettings` environment action. Use a hidden anchor
  `Window` so the action resolves reliably while the app uses `.accessory`
  activation policy; `SettingsPresenter` temporarily switches to `.regular`
  before opening and restores `.accessory` when the Settings view disappears.
