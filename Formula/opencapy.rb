# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.1"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.1/opencapy_darwin_amd64.tar.gz"
      sha256 "474095fe64778c4194e20967d8d1db41c52465ef5e3cb27dcd771d4122f17c05"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.1/opencapy_darwin_arm64.tar.gz"
      sha256 "923b67b298c6c7ede7648071cd7a7d88f7b7ac07c50462d3d20d0e2eadcabf26"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.1/opencapy_linux_amd64.tar.gz"
      sha256 "7c25c1da9b5926f3d9a612f99791b83c16380080a4e43bf60a2b77a3ac0b60a1"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.1/opencapy_linux_arm64.tar.gz"
      sha256 "f2dab9ba5c0a6ba785b0f9960b3326bf0ef00a5a1409e4a55a52c8507437f4fb"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
