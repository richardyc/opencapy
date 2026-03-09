# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.14"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.14/opencapy_darwin_amd64.tar.gz"
      sha256 "b80f2645a9975871c062425064f155c8a87827bfd68d233c32095d975e30f676"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.14/opencapy_darwin_arm64.tar.gz"
      sha256 "88dd6c395bce9242412294a701d39ff6ff828b2ff040fca3a4a0d163cfdd4656"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.14/opencapy_linux_amd64.tar.gz"
      sha256 "0702d93c87eafe586d1832ad433caf16a1b26bb2ed3206c27e929f49c0578827"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.14/opencapy_linux_arm64.tar.gz"
      sha256 "9a7231c884b51716d02c732218a2eb27df5c95482cb7a4ce74f52c30611b2604"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
