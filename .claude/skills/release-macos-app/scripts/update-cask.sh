#!/usr/bin/env bash
# update-cask.sh — bump Casks/filesnest.rb's version and sha256 to match a
# freshly built (and already notarized) DMG.
#
# Usage: update-cask.sh <version> <path-to-dmg>
set -euo pipefail

VERSION="${1:?usage: update-cask.sh <version> <path-to-dmg>}"
DMG_PATH="${2:?usage: update-cask.sh <version> <path-to-dmg>}"

[ -f "$DMG_PATH" ] || { echo "no such file: $DMG_PATH"; exit 1; }

REPO_ROOT="$(git -C "$(dirname "${BASH_SOURCE[0]}")" rev-parse --show-toplevel)"
CASK_FILE="$REPO_ROOT/Casks/filesnest.rb"
[ -f "$CASK_FILE" ] || { echo "missing $CASK_FILE"; exit 1; }

SHA256="$(shasum -a 256 "$DMG_PATH" | awk '{print $1}')"

sed -i '' -E "s/version \"[^\"]+\"/version \"${VERSION}\"/" "$CASK_FILE"
sed -i '' -E "s/sha256 \"[^\"]+\"/sha256 \"${SHA256}\"/" "$CASK_FILE"

echo "Updated $CASK_FILE"
echo "  version -> ${VERSION}"
echo "  sha256  -> ${SHA256}"
echo
echo "Review the diff, then:"
echo "  git add Casks/filesnest.rb"
echo "  git commit -m 'cask: bump filesnest to ${VERSION}'"
echo "  git push origin main"
