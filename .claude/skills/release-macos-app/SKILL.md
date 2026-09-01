---
name: release-macos-app
description: Cuts a new FilesNest macOS app release — bumps the version, archives a universal (Apple Silicon + Intel) build, signs it with the Developer ID Application certificate, notarizes and staples both the .app and the DMG, attaches the DMG to a GitHub Release, and updates the Homebrew cask at Casks/filesnest.rb. Use this whenever the user wants to release, ship, or publish a new version of the FilesNest macOS app, cut a build for testers, build/notarize a DMG, or update the Homebrew cask/tap for FilesNest — even if they only mention one piece of this (e.g. "just notarize it" or "bump the cask version") rather than asking for a full release by name.
---

# Release the FilesNest macOS app

This automates `docs/distribution/direct-distribution-signing.md` end to end
and adds the two steps that doc doesn't cover: attaching the result to a
GitHub Release and updating the Homebrew cask. Read that doc first if
anything below fails in a way this skill doesn't explain — it has the
narrative reasoning (why notarization is per-build, why the DMG itself needs
notarizing separately from the .app, etc.) that's stripped out here for
brevity.

**This process has real, hard-to-reverse side effects**: it submits builds to
Apple for notarization, pushes a git tag, creates/updates a public GitHub
Release, and commits+pushes a cask update. Confirm the version number and
run each phase below as a distinct step — don't chain phase 4 (publish) onto
phases 1–3 (build) without checking in, even if the user asked for "a
release" in one sentence. Building and signing is reversible (nothing public
happens); publishing is not.

## Prerequisites (one-time, per machine)

Same as `docs/distribution/direct-distribution-signing.md` §0: a `Developer
ID Application` certificate in the login keychain, and notarization
credentials stored under a keychain profile (default name `FN-NOTARY`,
override via the script's second argument if the user's is named
differently). `scripts/release.sh` checks for both up front and fails fast
with a pointer back to that doc if either is missing — don't try to
create/store credentials yourself, that's an interactive, one-time human
step involving Apple ID sign-in.

## Phase 1 — decide the version

Ask the user for the version if they didn't give one, or infer it from
context (e.g. "bump the patch version" against the last tag). FilesNest tags
are bare `X.Y.Z` — **no `v` prefix** (`git tag -l` shows `0.1.0` … `0.3.0`).
Check `gh release list --repo moontechs/files-nest` and `git tag` to confirm
the next version doesn't already exist.

## Phase 2 — build, sign, notarize

Run the bundled script from the repo root:

```bash
.claude/skills/release-macos-app/scripts/release.sh <version> [notary-profile]
```

It does, in order (matching the manual doc's Path B + DMG section):

1. Preflight: confirms `xcodebuild`, a Developer ID cert, and the notarytool
   profile all exist.
2. Bumps `MARKETING_VERSION` to `<version>` and increments
   `CURRENT_PROJECT_VERSION` (the build number) in `project.pbxproj`.
3. Archives via `xcodebuild archive` with `-destination 'generic/platform=macOS'`
   — this is what makes the archive universal (both Apple Silicon and Intel
   slices); Release builds have `ONLY_ACTIVE_ARCH` unset (defaults to `NO`),
   so this has been true since the project was set up, not something this
   script has to force.
4. Exports with `apple/macos/FilesNest/ExportOptions-developerid.plist`
   (Developer ID, team `MJVT445YNL`, already committed).
5. Zips, notarizes, and staples the `.app`.
6. Builds a DMG (`build/FilesNest-<version>.dmg`), signs it with the same
   Developer ID identity, notarizes, and staples the DMG itself (a DMG is
   separately Gatekeeper-assessed from the app inside it).
7. Verifies with `spctl` + `stapler validate` on both the app and the DMG —
   if either doesn't report "Notarized Developer ID" / "worked", the script
   has already failed loudly; do not proceed to phase 3 with a build that
   didn't pass this.

This phase touches nothing outside `build/` and the version bump in
`project.pbxproj` — safe to re-run if something goes wrong partway.

## Phase 3 — publish (confirm with the user before this phase)

1. Commit the version bump:
   ```bash
   git add apple/macos/FilesNest/FilesNest.xcodeproj/project.pbxproj
   git commit -m "release: FilesNest macOS <version>"
   ```
2. Tag and push:
   ```bash
   git tag <version>
   git push origin main --tags
   ```
3. Create or reuse the GitHub Release for that tag (the `new`/release-notes
   skill may have already created one from merged PRs — check with
   `gh release view <version>` first) and attach the DMG:
   ```bash
   gh release view <version> --repo moontechs/files-nest \
     || gh release create <version> --repo moontechs/files-nest --generate-notes
   gh release upload <version> build/FilesNest-<version>.dmg --repo moontechs/files-nest
   ```

## Phase 4 — update the Homebrew cask

```bash
.claude/skills/release-macos-app/scripts/update-cask.sh <version> build/FilesNest-<version>.dmg
git add Casks/filesnest.rb
git commit -m "cask: bump filesnest to <version>"
git push origin main
```

`update-cask.sh` computes the DMG's SHA-256 and rewrites the `version` and
`sha256` lines in `Casks/filesnest.rb` in place — see `Casks/README.md` for
how users actually tap and install this (full-URL tap, since the repo isn't
named `homebrew-*`).

## After publishing

Tell the user the release is live and give them the two install paths:

- Direct download: the DMG URL from the GitHub Release.
- Homebrew: `brew tap moontechs/files-nest https://github.com/moontechs/files-nest && brew install --cask filesnest` (first release) or `brew upgrade --cask filesnest` (subsequent ones).

If `spctl`/notarization failed partway through phase 2, don't attempt phases
3–4 — a build that isn't cleanly notarized shouldn't reach either
distribution channel; read the notarization log
(`xcrun notarytool log <submission-id> --keychain-profile <profile>`) and fix
the underlying issue instead of publishing anyway.
