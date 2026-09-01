cask "filesnest" do
  version "0.0.0"
  sha256 "0000000000000000000000000000000000000000000000000000000000000000000000000000"

  url "https://github.com/moontechs/files-nest/releases/download/#{version}/FilesNest-#{version}.dmg"
  name "FilesNest"
  desc "Self-hosted backup for iCloud Photos and Videos"
  homepage "https://github.com/moontechs/files-nest"

  depends_on macos: ">= :sonoma"

  app "FilesNest.app"

  zap trash: [
    "~/Library/Preferences/com.moontechs.FilesNest.plist",
    "~/Library/Application Support/FilesNest",
    "~/Library/Caches/com.moontechs.FilesNest",
  ]
end
