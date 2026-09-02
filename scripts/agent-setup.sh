#!/usr/bin/env bash
#
# One-time setup for a fresh checkout of files-nest, run automatically by
# coding-agent workers before they start (see CLAUDE.md/AGENTS.md). Wires up
# the local pre-commit gate and checks the tools it needs, so a broken
# environment fails loudly here instead of silently landing an unguarded
# commit that only CI catches.
#
# Run manually with: scripts/agent-setup.sh

set -u

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

status=0

git config core.hooksPath .githooks
configured="$(git config core.hooksPath)"
if [ "$configured" != ".githooks" ]; then
  printf '%s\n' \
    "ERROR: failed to set core.hooksPath — 'git config core.hooksPath' reports '$configured', expected '.githooks'." >&2
  status=1
fi

if [ ! -x "$repo_root/.githooks/pre-commit" ]; then
  printf '%s\n' \
    'ERROR: .githooks/pre-commit is not executable.' \
    'This is unexpected for a tracked file — investigate rather than chmod blindly' \
    '(a lost +x bit here usually means something upstream changed permissions).' >&2
  status=1
fi

if ! command -v golangci-lint >/dev/null 2>&1; then
  printf '%s\n' \
    'ERROR: golangci-lint was not found on your PATH.' \
    'The pre-commit hook requires it to lint server/ changes; install it now' \
    '(e.g. "brew install golangci-lint" or the official installer script)' \
    'so commits are gated locally instead of only in CI.' >&2
  status=1
fi

if ! command -v swift >/dev/null 2>&1; then
  if [ "$(uname -s)" = "Linux" ]; then
    printf '%s\n' 'swift not found — installing via swiftly (https://swift.org/install)...' >&2
    swiftly_dir="$(mktemp -d)"
    if curl -fsSL -o "$swiftly_dir/swiftly.tar.gz" "https://download.swift.org/swiftly/linux/swiftly-$(uname -m).tar.gz" \
      && tar zxf "$swiftly_dir/swiftly.tar.gz" -C "$swiftly_dir" \
      && "$swiftly_dir/swiftly" init --quiet-shell-followup -y >&2; then
      # shellcheck disable=SC1090
      . "${SWIFTLY_HOME_DIR:-$HOME/.local/share/swiftly}/env.sh"
    fi
    rm -rf "$swiftly_dir"
  fi

  if ! command -v swift >/dev/null 2>&1; then
    if [ "$(uname -s)" = "Linux" ]; then
      printf '%s\n' \
        'ERROR: swift is still not on PATH after attempting install via swiftly.' \
        'apple/ changes would go completely unguarded locally on this host.' \
        'Install the Swift toolchain by hand (https://swift.org/install), then re-run' \
        'scripts/agent-setup.sh.' >&2
      status=1
    else
      printf '%s\n' \
        'WARNING: swift was not found on your PATH.' \
        'apple/ changes will not be gated locally on this host (the Swift' \
        'toolchain is Xcode-only) — CI remains the only gate for that side.' >&2
    fi
  else
    printf '%s\n' "swift installed: $(swift --version 2>&1 | head -1)" >&2
  fi
fi

exit "$status"
