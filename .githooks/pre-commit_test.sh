#!/usr/bin/env bash
#
# Self-check for .githooks/pre-commit.
#
# Exercises the three behaviors the hook must guarantee:
#   1. a staged server/ file with a lint violation blocks the commit
#   2. staging only non-server/ files does not invoke golangci-lint and the
#      commit succeeds
#   3. a missing golangci-lint binary makes the hook fail closed with an
#      install hint (it never silently skips the gate)
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
# Scenario 2: staging only non-server/ files skips the linter; commit passes.
# ---------------------------------------------------------------------------
repo2="$tmp_root/repo2"
make_repo "$repo2"
printf '# test\n' > "$repo2/README.md"
git -C "$repo2" add README.md

# A stub golangci-lint that records (and fails) if it is ever invoked.
stub_bin="$tmp_root/stub-bin"
mkdir -p "$stub_bin"
cat > "$stub_bin/golangci-lint" <<'EOF'
#!/usr/bin/env bash
echo "golangci-lint was invoked" > "$STUB_INVOKED"
exit 99
EOF
chmod +x "$stub_bin/golangci-lint"
STUB_INVOKED="$tmp_root/stub-invoked"

if ! PATH="$stub_bin:$PATH" STUB_INVOKED="$STUB_INVOKED" \
     git -C "$repo2" -c core.hooksPath="$REPO_ROOT/.githooks" commit -m "docs only" >"$tmp_root/s2.log" 2>&1; then
  cat "$tmp_root/s2.log" >&2
  fail "scenario 2: docs-only commit should have succeeded"
fi
[ ! -e "$STUB_INVOKED" ] || fail "scenario 2: golangci-lint was invoked for a non-server/ commit"
pass "scenario 2: non-server/ commit succeeds without invoking golangci-lint"

# ---------------------------------------------------------------------------
# Scenario 3: missing golangci-lint fails closed with an install hint.
# ---------------------------------------------------------------------------
repo3="$tmp_root/repo3"
make_repo "$repo3"
mkdir -p "$repo3/server"
printf 'package main\n' > "$repo3/server/main.go"
git -C "$repo3" add server/

# Empty PATH: the hook must fail via bash builtins before needing git/grep.
# Invoke bash by absolute path so the empty PATH can't prevent launching it.
BASH_BIN="$(command -v bash)"
if (cd "$repo3" && PATH= "$BASH_BIN" "$HOOK") >"$tmp_root/s3.log" 2>&1; then
  cat "$tmp_root/s3.log" >&2
  fail "scenario 3: hook exited 0 with golangci-lint missing from PATH"
fi
grep -q 'golangci-lint was not found' "$tmp_root/s3.log" \
  || fail "scenario 3: install-hint message missing from hook output"
pass "scenario 3: missing golangci-lint fails closed with install hint"

printf '\nAll pre-commit hook checks passed.\n'
