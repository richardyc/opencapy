# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.16"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.16/opencapy_darwin_amd64.tar.gz"
      sha256 "0f2e97c7f40b770e611e0b9375ec53814983f4bfd3b4f40fe1f8008873409e9e"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.16/opencapy_darwin_arm64.tar.gz"
      sha256 "acdebefabf03d146fe3f8664b635c6d1748a0fb05944510c996a7a7e5f661c7e"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.16/opencapy_linux_amd64.tar.gz"
      sha256 "38de661d8491f3ef79ebd3b4d25c6033e492b19a16aec36efe6a51c30399081c"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.16/opencapy_linux_arm64.tar.gz"
      sha256 "d087ab1d619ca1c68c25ffe89c1b686b703562c6579a484e93a6eb7b5cd89139"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
