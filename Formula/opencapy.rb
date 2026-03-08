# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.9"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.9/opencapy_darwin_amd64.tar.gz"
      sha256 "296d3088539d6dc2a854ae953f5bf6e0d41a1b0cff91c5c33eaa01bb62ff9d55"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.9/opencapy_darwin_arm64.tar.gz"
      sha256 "ed7b6d83bbf24758b742184708073df65571bc25460e1f6f1fe80551504aee11"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.9/opencapy_linux_amd64.tar.gz"
      sha256 "1b84d29f14455075a6e6ed617d69f9f1f4937e676c16423d86724a1a03a14282"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.9/opencapy_linux_arm64.tar.gz"
      sha256 "22e0da02cf209bcf15957e6092509fe53347af56595a0d50937194b2ba032a75"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
