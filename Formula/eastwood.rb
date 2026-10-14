class Eastwood < Formula
  desc "Fast, pluggable source code linter"
  homepage "https://github.com/plutoniumm/eastwood"
  version "0.3.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_arm64.tar.gz"
      sha256 "ac4a889021d0ed84218929642b5878327c5708345af8a232d943c2359a4cd4c7"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_amd64.tar.gz"
      sha256 "9856a0e24939919322973ae288fc26d2470e1681645aef32cc626accebd8dc5b"
    end
  end

  def install
    bin.install "eastwood"
  end

  test do
    system "#{bin}/eastwood", "--version"
  end
end
