#!/usr/bin/env bash
#
# Self-check for .githooks/pre-commit.
#
# Exercises the behaviors the hook must guarantee:
#   1. a staged server/ file with a lint violation blocks the commit
#   2. staging only unrelated files does not invoke golangci-lint or
#      swift test, and the commit succeeds
#   3. a missing golangci-lint binary makes the hook fail closed with an
#      install hint (it never silently skips the gate)
#   4. a staged apple/ file with a failing swift test blocks the commit
#   5. a missing swift binary makes the hook fail closed with an install hint
#
# Run from the repo root:
#   ./.githooks/pre-commit_test.sh

set -u

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
HOOK="$REPO_ROOT/.githooks/pre-commit"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
pass() { printf 'ok: %s\n' "$*"; }

# --- sanity checks ----------------------------------------------------------
[ -x "$HOOK" ] || fail "hook is not executable: $HOOK"
command -v git >/dev/null 2>&1 || fail "git not on PATH (required for this test)"
command -v golangci-lint >/dev/null 2>&1 || fail "golangci-lint not on PATH (required for scenarios 1-2)"
command -v go >/dev/null 2>&1 || fail "go not on PATH (required for scenario 1)"

tmp_root="$(mktemp -d)"
trap 'rm -rf "$tmp_root"' EXIT

make_repo() {
  local dir="$1"
  mkdir -p "$dir"
  git -C "$dir" init -q
  git -C "$dir" config user.email "test@example.com"
  git -C "$dir" config user.name "pre-commit test"
}

# ---------------------------------------------------------------------------
# Scenario 1: a staged server/ file with a lint violation blocks the commit.
# ---------------------------------------------------------------------------
repo1="$tmp_root/repo1"
make_repo "$repo1"
mkdir -p "$repo1/server"
cat > "$repo1/server/go.mod" <<'EOF'
module testserver

go 1.21
EOF
# errcheck: the error return of os.Remove is intentionally not checked.
cat > "$repo1/server/main.go" <<'EOF'
package main

import "os"

func main() {
	os.Remove("/tmp/nonexistent")
}
EOF
git -C "$repo1" add server/

if git -C "$repo1" -c core.hooksPath="$REPO_ROOT/.githooks" commit -m "should be blocked" >"$tmp_root/s1.log" 2>&1; then
  cat "$tmp_root/s1.log" >&2
  fail "scenario 1: commit with a server/ lint violation was NOT blocked"
fi
grep -q 'golangci-lint reported violations' "$tmp_root/s1.log" \
  || fail "scenario 1: block message missing from hook output"
pass "scenario 1: staged server/ violation blocks the commit"

# ---------------------------------------------------------------------------
# Scenario 2: staging only unrelated files skips both linter and swift test;
# commit passes.
# ---------------------------------------------------------------------------
repo2="$tmp_root/repo2"
make_repo "$repo2"
printf '# test\n' > "$repo2/README.md"
git -C "$repo2" add README.md

# Stub binaries that record (and fail) if they are ever invoked.
stub_bin="$tmp_root/stub-bin"
mkdir -p "$stub_bin"
cat > "$stub_bin/golangci-lint" <<'EOF'
#!/usr/bin/env bash
echo "golangci-lint was invoked" > "$STUB_LINT_INVOKED"
exit 99
EOF
chmod +x "$stub_bin/golangci-lint"
cat > "$stub_bin/swift" <<'EOF'
#!/usr/bin/env bash
echo "swift was invoked" > "$STUB_SWIFT_INVOKED"
exit 99
EOF
chmod +x "$stub_bin/swift"
STUB_LINT_INVOKED="$tmp_root/stub-lint-invoked"
STUB_SWIFT_INVOKED="$tmp_root/stub-swift-invoked"

if ! PATH="$stub_bin:$PATH" STUB_LINT_INVOKED="$STUB_LINT_INVOKED" STUB_SWIFT_INVOKED="$STUB_SWIFT_INVOKED" \
     git -C "$repo2" -c core.hooksPath="$REPO_ROOT/.githooks" commit -m "docs only" >"$tmp_root/s2.log" 2>&1; then
  cat "$tmp_root/s2.log" >&2
  fail "scenario 2: docs-only commit should have succeeded"
fi
[ ! -e "$STUB_LINT_INVOKED" ] || fail "scenario 2: golangci-lint was invoked for an unrelated commit"
[ ! -e "$STUB_SWIFT_INVOKED" ] || fail "scenario 2: swift was invoked for an unrelated commit"
pass "scenario 2: unrelated commit succeeds without invoking golangci-lint or swift"

# ---------------------------------------------------------------------------
# Scenario 3: missing golangci-lint fails closed with an install hint.
# ---------------------------------------------------------------------------
repo3="$tmp_root/repo3"
make_repo "$repo3"
mkdir -p "$repo3/server"
printf 'package main\n' > "$repo3/server/main.go"
git -C "$repo3" add server/

# Minimal PATH (/usr/bin only, no Homebrew): git/grep/bash are present but
# golangci-lint is not, matching a real "linter not installed" machine.
BASH_BIN="$(command -v bash)"
if (cd "$repo3" && PATH="/usr/bin" "$BASH_BIN" "$HOOK") >"$tmp_root/s3.log" 2>&1; then
  cat "$tmp_root/s3.log" >&2
  fail "scenario 3: hook exited 0 with golangci-lint missing from PATH"
fi
grep -q 'golangci-lint was not found' "$tmp_root/s3.log" \
  || fail "scenario 3: install-hint message missing from hook output"
pass "scenario 3: missing golangci-lint fails closed with install hint"

# ---------------------------------------------------------------------------
# Scenario 4: a staged apple/ file with a failing swift test blocks the
# commit.
# ---------------------------------------------------------------------------
repo4="$tmp_root/repo4"
make_repo "$repo4"
mkdir -p "$repo4/apple/FilesNestCore"
printf 'placeholder\n' > "$repo4/apple/FilesNestCore/Package.swift"
mkdir -p "$repo4/apple/macos"
printf 'placeholder\n' > "$repo4/apple/macos/Some.swift"
git -C "$repo4" add apple/

# A stub swift, scoped to this scenario's PATH, that always fails.
stub_bin4="$tmp_root/stub-bin4"
mkdir -p "$stub_bin4"
cat > "$stub_bin4/swift" <<'EOF'
#!/usr/bin/env bash
[ "$1" = "test" ] || exit 0
exit 1
EOF
chmod +x "$stub_bin4/swift"

if PATH="$stub_bin4:$PATH" git -C "$repo4" -c core.hooksPath="$REPO_ROOT/.githooks" \
     commit -m "should be blocked" >"$tmp_root/s4.log" 2>&1; then
  cat "$tmp_root/s4.log" >&2
  fail "scenario 4: commit with a failing swift test was NOT blocked"
fi
grep -q 'swift test failed' "$tmp_root/s4.log" \
  || fail "scenario 4: block message missing from hook output"
pass "scenario 4: staged apple/ file with a failing swift test blocks the commit"

# ---------------------------------------------------------------------------
# Scenario 5: missing swift fails closed with an install hint.
# ---------------------------------------------------------------------------
repo5="$tmp_root/repo5"
make_repo "$repo5"
mkdir -p "$repo5/apple/FilesNestCore"
printf 'placeholder\n' > "$repo5/apple/FilesNestCore/Package.swift"
git -C "$repo5" add apple/

# /usr/bin/swift is a stub that errors without full Xcode installed, but its
# mere presence satisfies `command -v swift` — exclude it explicitly by
# building a minimal PATH from git/grep only.
minimal_bin="$tmp_root/minimal-bin"
mkdir -p "$minimal_bin"
ln -s "$(command -v git)" "$minimal_bin/git"
ln -s "$(command -v grep)" "$minimal_bin/grep"

if (cd "$repo5" && PATH="$minimal_bin" "$BASH_BIN" "$HOOK") >"$tmp_root/s5.log" 2>&1; then
  cat "$tmp_root/s5.log" >&2
  fail "scenario 5: hook exited 0 with swift missing from PATH"
fi
grep -q 'swift was not found' "$tmp_root/s5.log" \
  || fail "scenario 5: install-hint message missing from hook output"
pass "scenario 5: missing swift fails closed with install hint"

printf '\nAll pre-commit hook checks passed.\n'
