#!/usr/bin/env bash
#
# One-time setup for a fresh checkout of files-nest, run automatically by
# coding-agent workers before they start (see CLAUDE.md/AGENTS.md). Wires up
# the local pre-commit gate and checks the tools it needs, so a broken
# environment fails loudly here instead of silently landing an unguarded
# commit that only CI catches.
#
# Run manually with: scripts/agent-setup.sh

set -Eeuo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$repo_root" || exit 1

status=0

if ! command -v mise >/dev/null 2>&1; then
  printf '%s\n' \
    'ERROR: mise was not found on your PATH.' \
    'This repo pins tool versions (golangci-lint) in mise.toml; install mise' \
    '(https://mise.jdx.dev/getting-started.html) and re-run scripts/agent-setup.sh.' >&2
  status=1
else
  printf '%s\n' 'mise: installing pinned tools (mise install --include-task-tools)...' >&2
  if ! mise install --include-task-tools; then
    printf '%s\n' \
      'ERROR: "mise install --include-task-tools" failed.' \
      'Fix the reported tool install error above, then re-run scripts/agent-setup.sh.' >&2
    status=1
  fi
fi

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

# Resolve golangci-lint through mise rather than trusting PATH: shell
# activation isn't guaranteed in this process, and exporting PATH here
# wouldn't survive back to the parent shell anyway.
if command -v mise >/dev/null 2>&1; then
  if ! mise exec -- golangci-lint version >/dev/null 2>&1; then
    printf '%s\n' \
      'ERROR: golangci-lint is not available via "mise exec" after mise install.' \
      'The pre-commit hook requires it to lint server/ changes — check mise.toml' \
      'and re-run scripts/agent-setup.sh.' >&2
    status=1
  fi
elif ! command -v golangci-lint >/dev/null 2>&1; then
  printf '%s\n' \
    'ERROR: golangci-lint was not found on your PATH.' \
    'The pre-commit hook requires it to lint server/ changes; install it now' \
    '(e.g. "brew install golangci-lint" or the official installer script)' \
    'so commits are gated locally instead of only in CI.' >&2
  status=1
fi

# Not mise-managed: mise's core `swift` plugin has no Linux/arm64 build for
# most versions, so swiftly is the fallback on Linux (its runtime deps —
# libicu, libcurl, libedit, libncurses, etc. — must already be on the image;
# a non-root worker can't apt-get them here). GPG signature verification
# fails in this container even with gpg installed, so --no-verify is used;
# swift.org is fetched over HTTPS regardless.
if ! command -v swift >/dev/null 2>&1; then
  if [ "$(uname -s)" = "Linux" ]; then
    printf '%s\n' 'swift not found — installing latest stable via swiftly (https://swift.org/install)...' >&2
    swiftly_dir="$(mktemp -d)"
    if curl -fsSL -o "$swiftly_dir/swiftly.tar.gz" "https://download.swift.org/swiftly/linux/swiftly-$(uname -m).tar.gz" \
      && tar zxf "$swiftly_dir/swiftly.tar.gz" -C "$swiftly_dir" \
      && "$swiftly_dir/swiftly" init --skip-install --quiet-shell-followup -y \
      && . "${SWIFTLY_HOME_DIR:-$HOME/.local/share/swiftly}/env.sh" \
      && swiftly install latest --no-verify --assume-yes >&2; then
      :
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
else
  printf '%s\n' "swift installed: $(swift --version 2>&1 | head -1)" >&2
fi

exit "$status"
