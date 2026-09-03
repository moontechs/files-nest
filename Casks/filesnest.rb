cask "filesnest" do
  version "0.4.0"
  sha256 "ea363eb06ab8706b8278fc9bc5102dfe3f5b245eb79b14aec98c2c9b3ab289d3"

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
