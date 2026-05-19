class Eastwood < Formula
  desc "Fast, pluggable source code linter"
  homepage "https://github.com/plutoniumm/eastwood"
  version "0.2.3"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_arm64.tar.gz"
      sha256 "e515952202c4c4831e6d8c30206dc1a0261411896b0891581a4bdae2b4431915"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_amd64.tar.gz"
      sha256 "4eabf183a4f254fdff1645c876627c0b95448becd772a795f9068dd1077eed93"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_arm64.tar.gz"
      sha256 "24248c0249942b26f6a030bcec31b61f58b1eca80828bb51dae50cc619ebd754"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_amd64.tar.gz"
      sha256 "4afc333347bbcc8eb3b60c4904ebb3492ad85a4ff9bcbb38f1be0e0b9aed79aa"
    end
  end

  def install
    bin.install "eastwood"
  end

  test do
    system "#{bin}/eastwood", "--version"
  end
end
