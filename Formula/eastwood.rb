class Eastwood < Formula
  desc "Fast, pluggable source code linter"
  homepage "https://github.com/plutoniumm/eastwood"
  version "0.2.2"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_arm64.tar.gz"
      sha256 "0dbf17ee022b07f91a01d6db16d6262cfc2315a628c140d84425379034491c17"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_darwin_amd64.tar.gz"
      sha256 "4ef360ead4d78c765443b6a302194eeb3998f90856c7d9c48b5582c05bd9b5c4"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_arm64.tar.gz"
      sha256 "7e23efea1d685a60006dbd9cbfbb56b262c1fc0c3547e46441b63503f42f8dbe"
    end
    on_intel do
      url "https://github.com/plutoniumm/eastwood/releases/download/v#{version}/eastwood_linux_amd64.tar.gz"
      sha256 "581d1f7ee56cd8241dbba6376015d2f0efdc11a0b41ddc4b2a411eeec2a38a48"
    end
  end

  def install
    bin.install "eastwood"
  end

  test do
    system "#{bin}/eastwood", "--version"
  end
end
