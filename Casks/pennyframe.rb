cask "pennyframe" do
  version "0.3.1"
  sha256 "4d0c818b6060d461f9c38f78058df165a65fd9af760b22e28eae1afc7bbb2c41"

  url "https://github.com/Kollefranz/pennyframe/releases/download/v#{version}/pennyframe-#{version}-macos-universal.zip"
  name "Pennyframe"
  desc "Generate, compare and keep images from OpenRouter image models"
  homepage "https://github.com/Kollefranz/pennyframe"

  livecheck do
    url :url
    strategy :github_latest
  end

  app "Pennyframe.app"

  # The app is ad-hoc signed but not notarized. Clearing the download quarantine stops Gatekeeper
  # from refusing the first launch.
  postflight do
    system_command "/usr/bin/xattr",
                   args: ["-dr", "com.apple.quarantine", "#{appdir}/Pennyframe.app"]
  end

  zap trash: [
    "~/Library/Application Support/Pennyframe",
    "~/Library/Caches/Pennyframe",
  ]
end
