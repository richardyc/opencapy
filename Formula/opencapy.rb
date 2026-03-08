# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.13"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.13/opencapy_darwin_amd64.tar.gz"
      sha256 "5c7616828d5273cb7e36b65f0b2a2b95d99ae7e58ae1ac6cb17c765673f61fc4"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.13/opencapy_darwin_arm64.tar.gz"
      sha256 "470d092c87108dce0ff3a20a56daf48c78f4d572f5f669b466d214a67dd13218"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.13/opencapy_linux_amd64.tar.gz"
      sha256 "a1eff2d93e4e6f19da0c8be86640642ab8a7f2dd41e2385142ac4499e656a108"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.13/opencapy_linux_arm64.tar.gz"
      sha256 "41850f735da887ceb8d6df0e5ae173c4bb52101150a9d528b42d9f59117e0cb3"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
