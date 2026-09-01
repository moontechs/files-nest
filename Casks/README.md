# FilesNest Homebrew tap

This repo doubles as its own Homebrew tap — there's no separate
`homebrew-files-nest` repo. Because the repo name doesn't have the
`homebrew-` prefix Homebrew's short `brew tap user/name` form expects, users
tap it with the full URL:

```bash
brew tap moontechs/files-nest https://github.com/moontechs/files-nest
brew install --cask filesnest
```

`filesnest.rb` is updated automatically by the `release-macos-app` skill
(`.claude/skills/release-macos-app/`) as the last step of cutting a new
macOS release — it bumps `version` and `sha256` to match the DMG just
notarized and attached to the GitHub Release. Don't hand-edit the version
unless you're fixing a mistake; re-run the release skill instead so the
cask, the DMG, and the GitHub Release tag all stay in sync.
