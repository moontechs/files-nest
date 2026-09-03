# Design: KeychainStore

**Date:** 2026-07-26
**Status:** Approved, ready for planning
**Packages:** `apple/FilesNestCore` (new credential store + backend seam + fakes; no app-target work this slice)
**Depends on:**
- `docs/design/20260723-serverclient.md` (`CredentialStore` seam, `BasicCredentials`, merged `9eb5feb`) — §6 defines the protocol this unit conforms to.
- `docs/architecture.md` — "Credentials stored in macOS Keychain (`KeychainStore`). Never in UserDefaults or plists." (authoritative)

---

## 1. Purpose

The macOS/iOS app authenticates to the server with HTTP Basic Auth. `ServerClient`
consumes credentials only through the `CredentialStore` seam (`serverclient` §6);
tests inject a fake. This slice provides the real, Keychain-backed conformance so
the app can persist the user's single username/password securely.

Scope is exactly one credential: the server's Basic Auth `username` + `password`.
The **server URL is not stored here** — it is plain configuration and belongs with
settings state (the `SyncStateStore` / injected-`UserDefaults` pattern), not with a
secret. This unit never touches `UserDefaults` or plists.

## 2. Public surface

```swift
public struct KeychainStore: CredentialStore {
    public init(
        service: String = "com.filesnest.credentials",
        account: String = "basic-auth",
        backend: KeychainBackend = SystemKeychainBackend()
    )

    // CredentialStore conformance (read).
    public func basicCredentials() async throws -> BasicCredentials?

    // Write API (used by the later SettingsView slice; needed now to test read).
    public func save(_ credentials: BasicCredentials) throws
    public func clear() throws
}
```

- `basicCredentials()` is `async throws` per the seam: it fails loudly on an
  unexpected Keychain error rather than returning a bogus credential, and stays
  actor-agnostic (isolation-free `Sendable` struct).
- There is exactly one stored item, addressed by `service` + `account`. Both are
  injectable so tests never collide with the real login item.

## 3. The backend seam

Real `SecItem*` calls are wrapped behind a minimal protocol so `KeychainStore`'s
logic — query construction, add-vs-update, error mapping, en/decoding — is unit
tested against an in-memory fake, and the untestable syscall layer is a thin,
logic-free wrapper.

```swift
public protocol KeychainBackend: Sendable {
    func add(_ query: [String: Any]) -> OSStatus
    func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?)
    func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus
    func delete(_ query: [String: Any]) -> OSStatus
}

public struct SystemKeychainBackend: KeychainBackend { /* forwards to SecItem* */ }
```

`[String: Any]` is not `Sendable`; the protocol methods are synchronous and the
dictionaries never cross an isolation boundary (built and consumed within one
call), so this is sound. `KeychainStore` remains `Sendable` because its only
stored properties are `String`s and a `Sendable` `KeychainBackend`.

## 4. Storage model

- **Item class:** `kSecClassGenericPassword`.
- **Identity:** `kSecAttrService = service`, `kSecAttrAccount = account`. Fixed
  account ⇒ a single item; we do not use `kSecAttrAccount` for the *username*.
- **Secret payload:** the whole `BasicCredentials` (username **and** password) is
  JSON-encoded into `kSecValueData` as one opaque blob. Rationale: the username is
  part of the secret tuple, and keeping it in the encrypted value (rather than
  split into a searchable attribute like `kSecAttrAccount`) avoids leaking it into
  Keychain metadata and keeps read/write symmetric — one decode, one encode.
- **Accessibility:** `save` sets `kSecAttrAccessibleAfterFirstUnlock`, intending
  availability to background sync after the first unlock post-boot. Note the
  file-based keychain ignores this attribute — see below.
- **Keychain flavor:** the **legacy file-based keychain**, *not* the
  data-protection keychain. `kSecUseDataProtectionKeychain = true` would give
  iOS-parity semantics, but on macOS it requires a `keychain-access-groups`
  entitlement, which requires a provisioning profile — which a self-hosted,
  non-App-Store, Developer ID-signed app does not have. Trade-offs accepted: no
  Secure Enclave / biometric item protection (unused here), `kSecAttrAccessible`
  is ignored, and access is gated by a code-signature ACL (below). See TN3137.

### Code-signature ACLs and the re-signing prompt

The file-based keychain gates each item with an ACL bound to the **code signature
of the app that created it**. When the same logical app is later signed with a
*different* identity, macOS treats it as a different application and prompts:

> FilesNest wants to use your confidential information stored in
> "com.filesnest.credentials" in your keychain.

…requiring the login keychain password. **Always Allow** adds the new binary to
the item's ACL permanently; **Allow** grants one-time access and re-prompts on
the next launch.

This is expected behavior, not a defect, and it fires whenever the signing
identity changes:

- Going from a locally built (`Apple Development`) app to a distributed
  (`Developer ID Application`) one. First observed moving from dev builds to the
  notarized 0.4.0 DMG on 2026-09-03, against an item created 2026-08-22.
- Renewing or replacing the Developer ID certificate. The current one expires
  **2027-02-01**; the first release signed with its replacement will re-prompt
  every user who already has a stored credential.

Fresh installs never see it: with no pre-existing item, the app creates it and
owns the ACL from the start. Only users carrying a credential written by a
differently-signed build are affected.

### Operations

- `save`: build the base query, try `add`. On `errSecDuplicateItem`, fall back to
  `update` with the new `kSecValueData`. (Add-or-update, not delete-then-add, so a
  transient failure can't wipe an existing credential.)
- `basicCredentials`: `copyMatching` with `kSecReturnData = true`,
  `kSecMatchLimitOne`. `errSecItemNotFound` ⇒ `nil`. `errSecSuccess` ⇒ decode the
  `Data`; a decode failure is a thrown `decoding` error (corrupt item).
- `clear`: `delete`. `errSecItemNotFound` is treated as success (idempotent logout).

## 5. Errors

```swift
public enum KeychainStoreError: Error, Equatable {
    case unexpectedStatus(OSStatus)   // any SecItem status we don't special-case
    case decoding                     // stored blob wasn't decodable BasicCredentials
}
```

Special-cased statuses (`errSecSuccess`, `errSecItemNotFound`,
`errSecDuplicateItem`) are handled inline; everything else surfaces as
`unexpectedStatus` so callers/tests see the exact `OSStatus`.

## 6. Concurrency & Swift 6

- Compiles under Swift 6 language mode, complete concurrency checking, zero
  warnings.
- `KeychainStore` and `SystemKeychainBackend` are `Sendable` structs; no
  `@MainActor`, callable from any context. Pure `Security` + `Foundation`; no
  PhotoKit, SwiftUI, or app-target dependency.

## 7. Testing strategy

- **`FakeKeychainBackend`** (test target): an in-memory store — a class guarding a
  `[String: Data]` keyed by `service|account`, implementing add / copyMatching /
  update / delete with real Keychain-like status semantics (`errSecDuplicateItem`
  on add-existing, `errSecItemNotFound` on miss). Optionally forces a status to
  exercise the error path.
- **Coverage:**
  - save → `basicCredentials()` roundtrip returns the exact tuple.
  - save over an existing item updates in place (second value wins; no duplicate).
  - `basicCredentials()` on empty backend ⇒ `nil` (not a throw).
  - `clear()` removes the item; a subsequent read ⇒ `nil`; `clear()` on empty ⇒ no throw.
  - a forced unexpected `OSStatus` ⇒ `unexpectedStatus(that)` thrown.
  - corrupt stored bytes ⇒ `decoding` thrown.
- **One live test** using `SystemKeychainBackend` against the real Keychain with a
  unique test `service`, doing save→read→clear. Guarded to **skip** (not fail) when
  the environment is unentitled — i.e. `save` returns `errSecMissingEntitlement`
  (or a related entitlement/interaction-denied status) — so headless `swift test`
  in CI stays green while the path is still exercised on an entitled/dev machine.

## 8. Definition of done

- `KeychainStore` conforms to `CredentialStore`; `save`/`clear` implemented.
- `KeychainBackend` seam + `SystemKeychainBackend` wrapper.
- Add-or-update, not-found→`nil`, idempotent clear, typed error mapping.
- Builds in Swift 6 language mode with zero concurrency warnings.
- `swift test` green, covering §7; live Keychain test skips cleanly when unentitled.
- Zero dependency on PhotoKit, SwiftUI, or any app target — pure Foundation + Security.

## 9. Out of scope (future slices)

- `SettingsView` / first-launch credential entry + "Test Connection" (app-shell slice).
- Server-URL persistence (settings state, not a secret).
- Keychain access groups / app-extension sharing (only if a background extension needs it later).
- Credential migration from any prior storage (greenfield app; nothing to migrate).
