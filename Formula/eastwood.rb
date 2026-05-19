class Eastwood < Formula
  desc "Fast, pluggable source code linter"
  homepage "https://github.com/plutoniumm/eastwood"
  version "0.2.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_arm64.tar.gz"
      sha256 "6614348f25b4433b7f8c85826ed3c224e90c7899628fe5aa2160eb151478e098"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_amd64.tar.gz"
      sha256 "62797342cb7928ed259230cb97ef9b3a054b59e38362767e4ce963cd6bf8e4fb"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_arm64.tar.gz"
      sha256 "03e91b385e3a65f842fdf17469a9deaa56dcc641245f955074c6e4c3d9ef60f3"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_amd64.tar.gz"
      sha256 "45b4a139f962217b73dcf45588f3c2e665aab3ec4d5006ee05a80dfc1caf564e"
    end
  end

  def install
    bin.install "eastwood"
  end

  test do
    system "#{bin}/eastwood", "--version"
  end
end
