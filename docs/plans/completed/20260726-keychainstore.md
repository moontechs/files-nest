# KeychainStore Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a macOS/iOS Keychain-backed `CredentialStore` (`KeychainStore`) to `FilesNestCore` that securely persists the single HTTP Basic Auth credential.

**Architecture:** `KeychainStore` is a `Sendable` struct holding all logic (query building, add-or-update, error mapping, JSON en/decoding of the credential blob). Raw `SecItem*` calls sit behind a `KeychainBackend` protocol seam so the logic is unit-tested against an in-memory `FakeKeychainBackend`; `SystemKeychainBackend` is the thin real wrapper, exercised by one gated live test.

**Tech Stack:** Swift 6 language mode, Foundation + Security framework, swift-testing (`import Testing`), Swift Package Manager.

## Global Constraints

- Swift 6 language mode, complete concurrency checking, **zero warnings**.
- Pure `Foundation` + `Security`. No PhotoKit, SwiftUI, `UserDefaults`, plists, or any app-target dependency.
- All types `Sendable`; no `@MainActor`; isolation-free.
- Storage: `kSecClassGenericPassword`, `kSecUseDataProtectionKeychain = true`, `kSecAttrAccessibleAfterFirstUnlock`.
- Item identity is `service` + `account`, both injectable (defaults `"com.filesnest.credentials"` / `"basic-auth"`); the username is **not** a searchable attribute — the full credential is JSON-encoded into `kSecValueData`.
- Tests use swift-testing (`import Testing`, `@Test`, `#expect`), `@testable import FilesNestCore`. Fakes live in `Tests/FilesNestCoreTests/Support/`.
- Reference: `docs/design/20260726-keychainstore.md`.

---

### Task 1: Backend seam + KeychainStore core (save/read roundtrip)

**Files:**
- Create: `apple/FilesNestCore/Sources/FilesNestCore/KeychainStore.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeKeychainBackend.swift`
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift`

**Interfaces:**
- Consumes: `CredentialStore` (`func basicCredentials() async throws -> BasicCredentials?`) and `BasicCredentials` (`init(username:password:)`, `Equatable`), both existing in `FilesNestCore`.
- Produces:
  - `public protocol KeychainBackend: Sendable` with `add(_:) -> OSStatus`, `copyMatching(_:) -> (OSStatus, Any?)`, `update(_:_:) -> OSStatus`, `delete(_:) -> OSStatus` (all take `[String: Any]`).
  - `public struct SystemKeychainBackend: KeychainBackend` (`init()`).
  - `public struct KeychainStore: CredentialStore` with `init(service: String = "com.filesnest.credentials", account: String = "basic-auth", backend: KeychainBackend = SystemKeychainBackend())`, `func basicCredentials() async throws -> BasicCredentials?`, `func save(_ credentials: BasicCredentials) throws`, `func clear() throws`.
  - `public enum KeychainStoreError: Error, Equatable { case unexpectedStatus(OSStatus); case decoding }`.
  - Test-only `final class FakeKeychainBackend: KeychainBackend, @unchecked Sendable` with `var forcedStatus: OSStatus?`.

- [x] **Step 1: Write the failing test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeKeychainBackend.swift`:

```swift
import Foundation
@testable import FilesNestCore

/// In-memory stand-in for the real Keychain, keyed by service|account.
/// Reproduces the SecItem status semantics KeychainStore branches on.
final class FakeKeychainBackend: KeychainBackend, @unchecked Sendable {
    private let lock = NSLock()
    private var items: [String: Data] = [:]
    /// When set, every operation returns this status (drives error-path tests).
    var forcedStatus: OSStatus?

    init(forcedStatus: OSStatus? = nil) { self.forcedStatus = forcedStatus }

    private func key(_ query: [String: Any]) -> String {
        let service = query[kSecAttrService as String] as? String ?? ""
        let account = query[kSecAttrAccount as String] as? String ?? ""
        return "\(service)|\(account)"
    }

    func add(_ query: [String: Any]) -> OSStatus {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return forcedStatus }
        let k = key(query)
        if items[k] != nil { return errSecDuplicateItem }
        items[k] = query[kSecValueData as String] as? Data ?? Data()
        return errSecSuccess
    }

    func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?) {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return (forcedStatus, nil) }
        guard let data = items[key(query)] else { return (errSecItemNotFound, nil) }
        return (errSecSuccess, data)
    }

    func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return forcedStatus }
        let k = key(query)
        guard items[k] != nil else { return errSecItemNotFound }
        if let data = attributes[kSecValueData as String] as? Data { items[k] = data }
        return errSecSuccess
    }

    func delete(_ query: [String: Any]) -> OSStatus {
        lock.lock(); defer { lock.unlock() }
        if let forcedStatus { return forcedStatus }
        guard items.removeValue(forKey: key(query)) != nil else { return errSecItemNotFound }
        return errSecSuccess
    }
}
```

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift`:

```swift
import Testing
import Foundation
@testable import FilesNestCore

struct KeychainStoreTests {
    @Test func saveThenReadRoundTripsExactCredentials() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        try store.save(BasicCredentials(username: "alice", password: "s3cr3t:with/odd\"chars"))
        #expect(try await store.basicCredentials()
                == BasicCredentials(username: "alice", password: "s3cr3t:with/odd\"chars"))
    }
}
```

- [x] **Step 2: Run test to verify it fails**

Run: `cd apple/FilesNestCore && swift test --filter KeychainStoreTests`
Expected: FAIL — compile error, `KeychainBackend` / `KeychainStore` are undefined.

- [x] **Step 3: Write minimal implementation**

Create `apple/FilesNestCore/Sources/FilesNestCore/KeychainStore.swift`:

```swift
import Foundation
import Security

/// Seam over the raw `SecItem*` C API so `KeychainStore`'s logic is unit-testable
/// against an in-memory fake. Dictionaries are built and consumed within a single
/// synchronous call and never cross an isolation boundary, so `[String: Any]` is sound.
public protocol KeychainBackend: Sendable {
    func add(_ query: [String: Any]) -> OSStatus
    func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?)
    func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus
    func delete(_ query: [String: Any]) -> OSStatus
}

/// The real backend: a logic-free forwarder to Security framework.
public struct SystemKeychainBackend: KeychainBackend {
    public init() {}

    public func add(_ query: [String: Any]) -> OSStatus {
        SecItemAdd(query as CFDictionary, nil)
    }

    public func copyMatching(_ query: [String: Any]) -> (OSStatus, Any?) {
        var result: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &result)
        return (status, result)
    }

    public func update(_ query: [String: Any], _ attributes: [String: Any]) -> OSStatus {
        SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
    }

    public func delete(_ query: [String: Any]) -> OSStatus {
        SecItemDelete(query as CFDictionary)
    }
}

public enum KeychainStoreError: Error, Equatable {
    /// A `SecItem*` status we don't special-case; carries the exact `OSStatus`.
    case unexpectedStatus(OSStatus)
    /// The stored blob was present but not a decodable `BasicCredentials`.
    case decoding
}

/// `CredentialStore` conformance backed by the data-protection Keychain.
/// Stores exactly one Basic Auth credential, addressed by `service` + `account`.
public struct KeychainStore: CredentialStore {
    private let service: String
    private let account: String
    private let backend: KeychainBackend

    public init(
        service: String = "com.filesnest.credentials",
        account: String = "basic-auth",
        backend: KeychainBackend = SystemKeychainBackend()
    ) {
        self.service = service
        self.account = account
        self.backend = backend
    }

    /// The full credential tuple lives inside the encrypted `kSecValueData`, so the
    /// username never leaks into searchable Keychain metadata.
    private struct Stored: Codable {
        let username: String
        let password: String
    }

    private var baseQuery: [String: Any] {
        [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecUseDataProtectionKeychain as String: true,
        ]
    }

    public func basicCredentials() async throws -> BasicCredentials? {
        var query = baseQuery
        query[kSecReturnData as String] = true
        query[kSecMatchLimit as String] = kSecMatchLimitOne
        let (status, item) = backend.copyMatching(query)
        switch status {
        case errSecSuccess:
            guard let data = item as? Data else { throw KeychainStoreError.decoding }
            return try decode(data)
        case errSecItemNotFound:
            return nil
        default:
            throw KeychainStoreError.unexpectedStatus(status)
        }
    }

    public func save(_ credentials: BasicCredentials) throws {
        let data = try JSONEncoder().encode(
            Stored(username: credentials.username, password: credentials.password))
        var addQuery = baseQuery
        addQuery[kSecValueData as String] = data
        addQuery[kSecAttrAccessible as String] = kSecAttrAccessibleAfterFirstUnlock
        let addStatus = backend.add(addQuery)
        switch addStatus {
        case errSecSuccess:
            return
        case errSecDuplicateItem:
            let updateStatus = backend.update(baseQuery, [kSecValueData as String: data])
            guard updateStatus == errSecSuccess else {
                throw KeychainStoreError.unexpectedStatus(updateStatus)
            }
        default:
            throw KeychainStoreError.unexpectedStatus(addStatus)
        }
    }

    public func clear() throws {
        let status = backend.delete(baseQuery)
        switch status {
        case errSecSuccess, errSecItemNotFound:
            return
        default:
            throw KeychainStoreError.unexpectedStatus(status)
        }
    }

    private func decode(_ data: Data) throws -> BasicCredentials {
        do {
            let stored = try JSONDecoder().decode(Stored.self, from: data)
            return BasicCredentials(username: stored.username, password: stored.password)
        } catch {
            throw KeychainStoreError.decoding
        }
    }
}
```

- [x] **Step 4: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter KeychainStoreTests`
Expected: PASS (1 test). No warnings in build output.

- [x] **Step 5: Commit**

```bash
git add apple/FilesNestCore/Sources/FilesNestCore/KeychainStore.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/Support/FakeKeychainBackend.swift \
        apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift
git commit -m "feat: KeychainStore core with backend seam + save/read roundtrip"
```

---

### Task 2: Save is add-or-update (no duplicate item)

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift`

**Interfaces:**
- Consumes: `KeychainStore.save(_:)`, `KeychainStore.basicCredentials()`, `FakeKeychainBackend` (Task 1).
- Produces: nothing new.

- [x] **Step 1: Write the failing test**

Add inside `struct KeychainStoreTests`:

```swift
    @Test func saveTwiceUpdatesInPlaceSecondValueWins() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        try store.save(BasicCredentials(username: "alice", password: "first"))
        try store.save(BasicCredentials(username: "alice", password: "second"))
        #expect(try await store.basicCredentials()
                == BasicCredentials(username: "alice", password: "second"))
    }
```

- [x] **Step 2: Run test to verify it fails or passes**

Run: `cd apple/FilesNestCore && swift test --filter saveTwiceUpdatesInPlaceSecondValueWins`
Expected: PASS — the add-or-update path implemented in Task 1 already satisfies this. (This task locks the behavior with a regression test; if it fails, the `errSecDuplicateItem` → `update` branch is broken.)

- [x] **Step 3: Commit**

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift
git commit -m "test: KeychainStore save updates in place, no duplicate"
```

---

### Task 3: clear() and empty-read semantics

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift`

**Interfaces:**
- Consumes: `KeychainStore.save(_:)`, `.basicCredentials()`, `.clear()`, `FakeKeychainBackend` (Task 1).
- Produces: nothing new.

- [x] **Step 1: Write the failing test**

Add inside `struct KeychainStoreTests`:

```swift
    @Test func readOnEmptyReturnsNil() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        #expect(try await store.basicCredentials() == nil)
    }

    @Test func clearRemovesItemAndIsIdempotent() async throws {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend())
        try store.save(BasicCredentials(username: "alice", password: "pw"))
        try store.clear()
        #expect(try await store.basicCredentials() == nil)
        try store.clear() // second clear on empty must not throw
    }
```

- [x] **Step 2: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter KeychainStoreTests`
Expected: PASS (all tests). These lock in `errSecItemNotFound` → `nil` on read and the idempotent-clear branch from Task 1.

- [x] **Step 3: Commit**

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift
git commit -m "test: KeychainStore empty-read nil and idempotent clear"
```

---

### Task 4: Error mapping (unexpected status + decoding)

**Files:**
- Modify: `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift`

**Interfaces:**
- Consumes: `KeychainStore`, `KeychainStoreError`, `FakeKeychainBackend` (Task 1). `errSecIO` is a stand-in "unexpected" `OSStatus`.
- Produces: nothing new.

- [x] **Step 1: Write the failing test**

Add inside `struct KeychainStoreTests`:

```swift
    @Test func unexpectedStatusOnSaveThrowsMappedError() {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend(forcedStatus: errSecIO))
        #expect(throws: KeychainStoreError.unexpectedStatus(errSecIO)) {
            try store.save(BasicCredentials(username: "a", password: "b"))
        }
    }

    @Test func unexpectedStatusOnReadThrowsMappedError() async {
        let store = KeychainStore(service: "kc.test.\(UUID().uuidString)",
                                  backend: FakeKeychainBackend(forcedStatus: errSecIO))
        await #expect(throws: KeychainStoreError.unexpectedStatus(errSecIO)) {
            _ = try await store.basicCredentials()
        }
    }

    @Test func corruptStoredBytesThrowDecoding() async {
        // Seed the backend directly with non-JSON bytes under the store's key.
        let service = "kc.test.\(UUID().uuidString)"
        let backend = FakeKeychainBackend()
        _ = backend.add([
            kSecAttrService as String: service,
            kSecAttrAccount as String: "basic-auth",
            kSecValueData as String: Data([0x00, 0x01, 0x02]),
        ])
        let store = KeychainStore(service: service, backend: backend)
        await #expect(throws: KeychainStoreError.decoding) {
            _ = try await store.basicCredentials()
        }
    }
```

- [x] **Step 2: Run test to verify it passes**

Run: `cd apple/FilesNestCore && swift test --filter KeychainStoreTests`
Expected: PASS (all tests). Exercises the `default:` → `unexpectedStatus` branches and the `decode` failure path from Task 1.

- [x] **Step 3: Commit**

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreTests.swift
git commit -m "test: KeychainStore error mapping for unexpected status and corrupt bytes"
```

---

### Task 5: Live Keychain roundtrip, gated on entitlement

**Files:**
- Create: `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreLiveTests.swift`

**Interfaces:**
- Consumes: `KeychainStore` with default `SystemKeychainBackend`, `KeychainStoreError` (Task 1).
- Produces: nothing new.

- [x] **Step 1: Write the test**

Create `apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreLiveTests.swift`:

```swift
import Testing
import Foundation
import Security
@testable import FilesNestCore

/// Exercises the real Security-framework backend end to end. In an unentitled
/// headless environment (CI `swift test`), the Keychain rejects the write with an
/// entitlement/interaction status; we treat that as "skip", not "fail", so the
/// path is still verified on an entitled/dev machine.
struct KeychainStoreLiveTests {
    @Test func liveSaveReadClearRoundTrip() async throws {
        let service = "kc.live.\(UUID().uuidString)"
        let store = KeychainStore(service: service) // default SystemKeychainBackend
        let creds = BasicCredentials(username: "live-user", password: "live-pass")

        do {
            try store.save(creds)
        } catch let KeychainStoreError.unexpectedStatus(status)
            where status == errSecMissingEntitlement || status == errSecInteractionNotAllowed {
            // Unentitled runner (e.g. headless CI) — skip cleanly.
            return
        }

        // Ensure we always clean up the real Keychain item.
        defer { try? store.clear() }

        #expect(try await store.basicCredentials() == creds)
        try store.clear()
        #expect(try await store.basicCredentials() == nil)
    }
}
```

- [x] **Step 2: Run the test**

Run: `cd apple/FilesNestCore && swift test --filter KeychainStoreLiveTests`
Expected: PASS — either a full live roundtrip (entitled/dev machine) or an early return (unentitled). Either way the test is green and leaves no Keychain item behind.

- [x] **Step 3: Run the full package suite**

Run: `cd apple/FilesNestCore && swift test`
Expected: PASS — entire `FilesNestCore` suite green, no build warnings.

- [x] **Step 4: Commit**

```bash
git add apple/FilesNestCore/Tests/FilesNestCoreTests/KeychainStoreLiveTests.swift
git commit -m "test: KeychainStore live Keychain roundtrip, gated on entitlement"
```

---

## Definition of Done

- `KeychainStore` conforms to `CredentialStore`; `save`/`clear` implemented.
- `KeychainBackend` seam + `SystemKeychainBackend` wrapper.
- Add-or-update, not-found→`nil`, idempotent clear, typed error mapping (`unexpectedStatus`, `decoding`).
- `swift test` green; live Keychain test skips cleanly when unentitled.
- Builds in Swift 6 language mode, zero concurrency warnings; pure Foundation + Security.
- Open PR titled `Apple clients: KeychainStore (#5)`.
