cask "pennyframe" do
  version "0.3.3"
  sha256 "6d850ceadcdcb66ebd7c08a8ec00290e7c7cb57e5f23634d64bcc83b5a5d1c6e"

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
