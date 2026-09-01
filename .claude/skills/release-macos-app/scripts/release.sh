#!/usr/bin/env bash
# release.sh — build, sign (Developer ID), notarize, and staple a FilesNest
# macOS release: archive -> export -> notarize+staple .app -> DMG ->
# sign+notarize+staple DMG -> verify.
#
# This is the scripted form of docs/distribution/direct-distribution-signing.md
# (Path B + the DMG packaging section). Read that doc for the "why" behind
# each step; this script is deliberately just the "what".
#
# Usage: release.sh <version e.g. 0.4.0> [notarytool-keychain-profile]
set -euo pipefail

VERSION="${1:?usage: release.sh <version e.g. 0.4.0> [notarytool-profile]}"
NOTARY_PROFILE="${2:-FN-NOTARY}"

REPO_ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
APPLE_DIR="$REPO_ROOT/apple/macos/FilesNest"
PROJECT_DIR="$APPLE_DIR/FilesNest.xcodeproj"
PBXPROJ="$PROJECT_DIR/project.pbxproj"
EXPORT_OPTIONS="$APPLE_DIR/ExportOptions-developerid.plist"

BUILD_DIR="$REPO_ROOT/build"
ARCHIVE_PATH="$BUILD_DIR/FilesNest.xcarchive"
EXPORT_DIR="$BUILD_DIR/export"
APP_PATH="$EXPORT_DIR/FilesNest.app"
ZIP_PATH="$BUILD_DIR/FilesNest.zip"
DMG_PATH="$BUILD_DIR/FilesNest-${VERSION}.dmg"

echo "==> 1/8 preflight"
command -v xcodebuild >/dev/null || { echo "xcodebuild not found — install Xcode command line tools"; exit 1; }
command -v xcrun >/dev/null || { echo "xcrun not found"; exit 1; }
[ -f "$EXPORT_OPTIONS" ] || { echo "missing $EXPORT_OPTIONS"; exit 1; }
security find-identity -v -p codesigning | grep -q "Developer ID Application" \
  || { echo "No 'Developer ID Application' certificate in the login keychain — see docs/distribution/direct-distribution-signing.md §0"; exit 1; }
xcrun notarytool history --keychain-profile "$NOTARY_PROFILE" >/dev/null 2>&1 \
  || { echo "notarytool keychain profile '$NOTARY_PROFILE' not found — store it first (see the doc §0), or pass the right profile name as the 2nd argument"; exit 1; }

echo "==> 2/8 bump version to ${VERSION}"
CURRENT_BUILD="$(grep -m1 -oE 'CURRENT_PROJECT_VERSION = [0-9]+;' "$PBXPROJ" | grep -oE '[0-9]+')"
NEXT_BUILD=$((CURRENT_BUILD + 1))
sed -i '' -E "s/MARKETING_VERSION = [0-9]+(\.[0-9]+)*;/MARKETING_VERSION = ${VERSION};/g" "$PBXPROJ"
sed -i '' -E "s/CURRENT_PROJECT_VERSION = [0-9]+;/CURRENT_PROJECT_VERSION = ${NEXT_BUILD};/g" "$PBXPROJ"
echo "    MARKETING_VERSION -> ${VERSION}, CURRENT_PROJECT_VERSION -> ${NEXT_BUILD}"

echo "==> 3/8 archive (universal: Apple Silicon + Intel)"
rm -rf "$ARCHIVE_PATH"
xcodebuild -project "$PROJECT_DIR" -scheme FilesNest -configuration Release \
  -destination 'generic/platform=macOS' -archivePath "$ARCHIVE_PATH" \
  -allowProvisioningUpdates archive

echo "==> 4/8 export (Developer ID)"
rm -rf "$EXPORT_DIR"
xcodebuild -exportArchive -archivePath "$ARCHIVE_PATH" \
  -exportOptionsPlist "$EXPORT_OPTIONS" \
  -exportPath "$EXPORT_DIR"
[ -d "$APP_PATH" ] || { echo "export did not produce $APP_PATH"; exit 1; }

echo "==> 5/8 notarize + staple the .app"
rm -f "$ZIP_PATH"
ditto -c -k --keepParent "$APP_PATH" "$ZIP_PATH"
xcrun notarytool submit "$ZIP_PATH" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$APP_PATH"

echo "==> 6/8 build + sign the DMG"
rm -f "$DMG_PATH"
hdiutil create -volname FilesNest -srcfolder "$APP_PATH" -ov -format UDZO "$DMG_PATH"
DEV_ID="$(security find-identity -v -p codesigning | grep "Developer ID Application" | head -1 | sed -E 's/.*"(.*)"/\1/')"
codesign --force --sign "$DEV_ID" "$DMG_PATH"

echo "==> 7/8 notarize + staple the DMG"
xcrun notarytool submit "$DMG_PATH" --keychain-profile "$NOTARY_PROFILE" --wait
xcrun stapler staple "$DMG_PATH"

echo "==> 8/8 verify"
spctl -a -vvv -t install "$APP_PATH"
xcrun stapler validate "$APP_PATH"
spctl -a -vvv -t open --context context:primary-signature "$DMG_PATH"

echo
echo "Build + notarization succeeded: $DMG_PATH"
echo
echo "Next (confirm with the user before publishing anything):"
echo "  git add ${PBXPROJ#"$REPO_ROOT"/}"
echo "  git commit -m 'release: FilesNest macOS ${VERSION}'"
echo "  git tag ${VERSION} && git push origin main --tags"
echo "  gh release create ${VERSION} --repo moontechs/files-nest --generate-notes"
echo "  gh release upload ${VERSION} ${DMG_PATH} --repo moontechs/files-nest"
echo "  .claude/skills/release-macos-app/scripts/update-cask.sh ${VERSION} ${DMG_PATH}"
