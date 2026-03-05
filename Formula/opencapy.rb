class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.1.0"
  license "MIT"

  on_macos do
    on_arm do
      url "https://github.com/richardyc/opencapy/releases/download/v0.1.0/opencapy_darwin_arm64.tar.gz"
      sha256 "placeholder"
    end
    on_intel do
      url "https://github.com/richardyc/opencapy/releases/download/v0.1.0/opencapy_darwin_amd64.tar.gz"
      sha256 "placeholder"
    end
  end

  on_linux do
    on_arm do
      url "https://github.com/richardyc/opencapy/releases/download/v0.1.0/opencapy_linux_arm64.tar.gz"
      sha256 "placeholder"
    end
    on_intel do
      url "https://github.com/richardyc/opencapy/releases/download/v0.1.0/opencapy_linux_amd64.tar.gz"
      sha256 "placeholder"
    end
  end

  def install
    bin.install "opencapy"
  end

  test do
    system "#{bin}/opencapy", "status"
  end
end
