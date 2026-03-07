# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.2"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.2/opencapy_darwin_amd64.tar.gz"
      sha256 "e5fb1f930fccee12860cf67fdb4bf2f69df446ba18994599bd19378fd0f44f72"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.2/opencapy_darwin_arm64.tar.gz"
      sha256 "f7ab2f4e87c8a06950df5ef242541a22aba1b6d919373ecc100e413b25706942"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.2/opencapy_linux_amd64.tar.gz"
      sha256 "97341f5f7565f9374e81d6e6faa30809f458dd04129d153f8ce5bc62fcde9e34"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.2/opencapy_linux_arm64.tar.gz"
      sha256 "cec222a05c7befa39feb4c9e0b1a4c361904f4c60833cee41241012267dd0fcf"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
