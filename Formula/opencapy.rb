# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.10"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.10/opencapy_darwin_amd64.tar.gz"
      sha256 "0ea08ec9b48119dc720c749550f80768b11068481a764e3ec4d68007d45ed8ea"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.10/opencapy_darwin_arm64.tar.gz"
      sha256 "f4b256560e86413fe2276cccfe7d6fad7bdbaa126a5ba0221b4f4213f9b09fb1"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.10/opencapy_linux_amd64.tar.gz"
      sha256 "8c535c5533c4522c9525101e5e6358de36bdbaf4a3681f8e62b1342223a5fc8b"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.10/opencapy_linux_arm64.tar.gz"
      sha256 "5186411ccfc9306684d575a8bdc76185ddd3c1f5e5a56856d8106cf97a4ae5c7"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
