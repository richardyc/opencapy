# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.5"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.5/opencapy_darwin_amd64.tar.gz"
      sha256 "ba72be946d06f0b2605319b6add45711afb3bdb0b7bd8ca2e8d677e671182146"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.5/opencapy_darwin_arm64.tar.gz"
      sha256 "c3db34eeef33668fe3f47354a86f30e67bdc605ebc7ca93d1f7143c152e62d9d"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.5/opencapy_linux_amd64.tar.gz"
      sha256 "6e08a5d3f31638e09e2c5094376239fd958be48b2066d170f7dddb2384862443"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.5/opencapy_linux_arm64.tar.gz"
      sha256 "bd7445168bd3f228c04c5480df8ce71a1882833dc0095404eebaef631e33187a"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
