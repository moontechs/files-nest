cask "filesnest" do
  version "0.4.2"
  sha256 "3dbd7e4e1763aebdace87a1d8230965a5ffd45160073495eda0b79d86d06666a"

  url "https://github.com/moontechs/files-nest/releases/download/#{version}/FilesNest-#{version}.dmg"
  name "FilesNest"
  desc "Self-hosted backup for iCloud Photos and Videos"
  homepage "https://github.com/moontechs/files-nest"

  # Deliberately the deprecated string form. Homebrew < 6 parses a bare
  # `:sonoma` as "== Sonoma", so the modern form would refuse to install on
  # Sequoia or newer for anyone who hasn't updated Homebrew. The string form
  # means ">= Sonoma" on every version; it only costs a deprecation warning.
  depends_on macos: ">= :sonoma"

  app "FilesNest.app"

  zap trash: [
    "~/Library/Preferences/com.moontechs.FilesNest.plist",
    "~/Library/Application Support/FilesNest",
    "~/Library/Caches/com.moontechs.FilesNest",
  ]
end
