cask "pennyframe" do
  version "0.3.2"
  sha256 "e7c96dad05249bcfddc96cddc3f00be0989b9549d7ab72bce40c9d5d9e4ac74d"

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
