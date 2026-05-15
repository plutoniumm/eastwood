class Eastwood < Formula
  desc "Fast, pluggable source code linter"
  homepage "https://github.com/plutoniumm/eastwood"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_arm64.tar.gz"
      sha256 "PLACEHOLDER_darwin_arm64"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_amd64.tar.gz"
      sha256 "PLACEHOLDER_darwin_amd64"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_arm64.tar.gz"
      sha256 "PLACEHOLDER_linux_arm64"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_amd64.tar.gz"
      sha256 "PLACEHOLDER_linux_amd64"
    end
  end

  def install
    bin.install "eastwood"
  end

  test do
    system "#{bin}/eastwood", "--version"
  end
end
