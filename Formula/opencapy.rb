# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.8"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.8/opencapy_darwin_amd64.tar.gz"
      sha256 "b3b79f0c86a3ae312ac3f37313bf3bb59e5de996098c445a6103f549f82b19d4"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.8/opencapy_darwin_arm64.tar.gz"
      sha256 "fb9cb6f2ec0d78806f4d547f7b0a93159b8bdb15443be13daac086a74ce8a6ee"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.8/opencapy_linux_amd64.tar.gz"
      sha256 "154c08674a958b95e5755f6f2ffbe5a11a54189d4df735851c2f2694442ebcd5"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.8/opencapy_linux_arm64.tar.gz"
      sha256 "9dbeb74ec1bcc2b3fd5cc55fb45c9fc05fb296e0cd4e43c5b49ffc5e742c169d"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
