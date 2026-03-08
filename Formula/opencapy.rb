# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.12"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.12/opencapy_darwin_amd64.tar.gz"
      sha256 "c889a0c27a57ff720d24aec91208bf1cad8e4f8f00f2951dfa41bc1032eed4fc"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.12/opencapy_darwin_arm64.tar.gz"
      sha256 "e301fbf0972c5760f4fa524fb158973d43e534623c65f39b7e846f3cf715a4d1"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.12/opencapy_linux_amd64.tar.gz"
      sha256 "bcb1329ababef6d306df3cf62e44cbab32341c6ce0d1b30dad189a632c115e2b"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.12/opencapy_linux_arm64.tar.gz"
      sha256 "22791fd1521455c148aed75a39416a2b24fd68a0f8f3ea6a2a9e5ad0aff437c8"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
