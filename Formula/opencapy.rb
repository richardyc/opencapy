# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.11"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.11/opencapy_darwin_amd64.tar.gz"
      sha256 "ce6986510c8081cab9330f2c614605cd9612d6973ca358e218d73ee5d46c5cce"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.11/opencapy_darwin_arm64.tar.gz"
      sha256 "61dc836187f76924782dcf6cf98633d66fbdb07eeef469b6a052d0a30ae778ac"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.11/opencapy_linux_amd64.tar.gz"
      sha256 "e03a9a50027ecdb13414b6d6c3d683b329926d500f59df6453ba9d593f7fb1ee"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.11/opencapy_linux_arm64.tar.gz"
      sha256 "82ac4813b6dd35bb67fcd851ff82d05c0b7fa55616999f24af31b48a51a68d7c"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
