# FilesNest Homebrew tap

This repo doubles as its own Homebrew tap — there's no separate
`homebrew-files-nest` repo. Because the repo name doesn't have the
`homebrew-` prefix Homebrew's short `brew tap user/name` form expects, users
tap it with the full URL:

```bash
brew tap moontechs/files-nest https://github.com/moontechs/files-nest
brew trust --cask moontechs/files-nest/filesnest
brew install --cask filesnest
```

`brew trust` applies to **Homebrew 6.0+ only** (the command and its
enforcement both landed 2026-05-30). Homebrew 6 refuses to load casks from
non-official taps until they're explicitly trusted, failing with "Refusing to
load cask ... from untrusted tap"; nothing in the cask can waive that, since
it's a deliberate user-side control recorded in `~/.homebrew/trust.json`. Use
`brew trust moontechs/files-nest` to trust the whole tap rather than just this
cask.

On Homebrew 5.x and older the command doesn't exist (`Unknown command: trust`)
and no trust is needed — skip that line and tap/install directly.

## Why `depends_on` uses the deprecated string form

`filesnest.rb` intentionally declares `depends_on macos: ">= :sonoma"` even
though Homebrew 6 warns that the string comparison format is deprecated and
suggests `depends_on macos: :sonoma`. **Do not apply that suggestion yet.**
The two forms are not equivalent across versions: Homebrew changed cask
`depends_on macos:` semantics on 2026-05-08, and before that a bare symbol
parsed as `== sonoma`, not `>= sonoma`. Switching would make the cask refuse
to install on Sequoia or newer for anyone running an older Homebrew. The
string form means `>= Sonoma` on every version and costs only a warning.
Migrate once the string form is actually removed, or once we're willing to
require Homebrew 6+.

`filesnest.rb` is updated automatically by the `release-macos-app` skill
(`.claude/skills/release-macos-app/`) as the last step of cutting a new
macOS release — it bumps `version` and `sha256` to match the DMG just
notarized and attached to the GitHub Release. Don't hand-edit the version
unless you're fixing a mistake; re-run the release skill instead so the
cask, the DMG, and the GitHub Release tag all stay in sync.
