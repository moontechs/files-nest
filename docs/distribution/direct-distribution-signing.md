# Direct distribution: Developer ID + notarization

How to build a FilesNest macOS build you can hand directly to testers (a
notarized `.app` in a DMG they drag to `/Applications`). This is **direct
distribution** — outside the App Store / TestFlight — via a **Developer ID
Application** signature and Apple **notarization**, so Gatekeeper lets it launch
on testers' Macs with no right-click-open workaround.

The project is already set up for this: **hardened runtime is ON** (required for
notarization), it's sandboxed, and the Photos entitlement + usage string are
present. Do not disable those.

Project facts (from `FilesNest.xcodeproj` / `FilesNest.entitlements`):
- Bundle id: `com.moontechs.FilesNest`
- `ENABLE_HARDENED_RUNTIME = YES`, `ENABLE_APP_SANDBOX = YES`, macOS 14 deployment
- Entitlements: `com.apple.security.app-sandbox`, `…network.client`,
  `…personal-information.photos-library`
- `NSPhotoLibraryUsageDescription` set

## 0. Prerequisites (do these once)

- [x] **Apple Developer Program membership active, with the Program License
      Agreement accepted.** ✅ Confirmed: the **Apple Developer Program License
      Agreement** is accepted (latest version issued 2026-03-31, accepted 2026-04-07)
      — so certificate creation and `notarytool` submission are unblocked. (The
      earlier `errSecMissingEntitlement` pain was about `keychain-access-groups`
      needing a *provisioning profile* — unrelated to Developer ID direct
      distribution, which needs no profile.) The separate **Paid Applications
      Agreement** is **not required** here — it only gates selling paid apps / IAP on
      the App Store. Re-check at developer.apple.com/account → **Agreements** if Apple
      later issues a new PLA version (it must be re-accepted before notarization).
- [ ] A **Developer ID Application** certificate in your login keychain (NOT
      "Apple Development"). Create it in Xcode → Settings → Accounts → your team →
      *Manage Certificates* → **+** → *Developer ID Application*.
- [ ] Your **Team ID** (developer.apple.com/account → Membership).
- [ ] An **app-specific password** for notarization (appleid.apple.com → Sign-In &
      Security → App-Specific Passwords).
- [ ] Store notarization credentials once:
      ```bash
      xcrun notarytool store-credentials FN-NOTARY \
        --apple-id "you@example.com" --team-id "YOUR_TEAM_ID" \
        --password "abcd-efgh-ijkl-mnop"   # the app-specific password
      ```

## Path A — Xcode Organizer (simplest, recommended the first time)

1. Target → *Signing & Capabilities*: Team = your team (Automatic signing is fine).
2. **Product → Archive** (destination: *Any Mac (Apple Silicon, Intel)*, Release).
3. In the **Organizer**: *Distribute App* → **Direct Distribution**. Xcode signs
   with Developer ID, uploads for notarization, waits, and **staples**
   automatically, then exports the notarized `.app`.
4. Package it for testers (§3) and verify (§4).

## Path B — CLI (scriptable, repeatable releases)

1. **Archive**:
   ```bash
   xcodebuild -project apple/macos/FilesNest/FilesNest.xcodeproj \
     -scheme FilesNest -configuration Release \
     -destination 'generic/platform=macOS' \
     -archivePath build/FilesNest.xcarchive \
     DEVELOPMENT_TEAM=YOUR_TEAM_ID -allowProvisioningUpdates archive
   ```

2. **Export** with Developer ID. Create `ExportOptions.plist`:
   ```xml
   <?xml version="1.0" encoding="UTF-8"?>
   <!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
   <plist version="1.0"><dict>
     <key>method</key><string>developer-id</string>
     <key>teamID</key><string>YOUR_TEAM_ID</string>
     <key>signingStyle</key><string>automatic</string>
   </dict></plist>
   ```
   ```bash
   xcodebuild -exportArchive -archivePath build/FilesNest.xcarchive \
     -exportOptionsPlist ExportOptions.plist -exportPath build/export
   ```
   → `build/export/FilesNest.app`, signed with Developer ID + hardened runtime.

3. **Notarize + staple**:
   ```bash
   ditto -c -k --keepParent build/export/FilesNest.app build/FilesNest.zip
   xcrun notarytool submit build/FilesNest.zip --keychain-profile FN-NOTARY --wait
   # on "status: Accepted":
   xcrun stapler staple build/export/FilesNest.app
   ```
   If it's rejected, read the log:
   `xcrun notarytool log <submission-id> --keychain-profile FN-NOTARY`.

## 3. Package for testers (DMG)

```bash
hdiutil create -volname FilesNest \
  -srcfolder build/export/FilesNest.app \
  -ov -format UDZO build/FilesNest.dmg
xcrun stapler staple build/FilesNest.dmg   # optional but nice
```
(A zip works too — `ditto -c -k --keepParent FilesNest.app FilesNest.zip` — but a
DMG gives testers a clean drag-to-Applications window.)

## 4. Verify before sending

```bash
spctl -a -vvv -t install build/export/FilesNest.app
#   → accepted; source=Notarized Developer ID
xcrun stapler validate build/export/FilesNest.app
#   → The validate action worked!
codesign -dvvv --entitlements - build/export/FilesNest.app
#   → Authority=Developer ID Application: … ; flags=…runtime… ; the 3 entitlements
```
All three must pass. If `spctl` says anything but "Notarized Developer ID", do not
send it.

## 5. Tester install & first run

1. Send the DMG (or zip) + the server URL and their credentials.
2. Tester opens the DMG, drags **FilesNest.app → Applications**, launches it.
   Because it's notarized + stapled, Gatekeeper allows it directly (no
   right-click → Open).
3. First launch: grant the **Photos** permission prompt; open the menu-bar panel
   and sign in with the server URL. The app is a menu-bar agent (no Dock icon).

## Gotchas

- **Notarization is per build.** Every build you send must be signed + notarized +
  stapled, or Gatekeeper blocks it on the tester's Mac.
- **Bump the build number** each release (`CURRENT_PROJECT_VERSION` / `CFBundleVersion`)
  so testers (and you) can tell builds apart.
- **Keep hardened runtime and the entitlements.** Disabling hardened runtime breaks
  notarization; dropping `personal-information.photos-library` /
  `NSPhotoLibraryUsageDescription` breaks Photos access.
- **Menu-bar polish (optional):** the app becomes an agent at runtime
  (`NSApp.setActivationPolicy(.accessory)`), so a Dock icon may briefly flash at
  launch. Add `LSUIElement = YES` ("Application is agent") to the Info.plist for a
  clean agent launch. Not a blocker.
- **No auto-update.** Testers re-download new DMGs manually; there's no Sparkle-style
  updater yet. Tell them when a new build is available.
- **TestFlight is the alternative** (in-app updates, crash reports, tester management)
  but needs App Store Connect signing + the agreements resolved; direct distribution
  is the faster path to first testers.
