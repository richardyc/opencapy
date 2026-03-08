# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.4"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.4/opencapy_darwin_amd64.tar.gz"
      sha256 "21cd5f1eeb199567f9f5bd5f635897c0f51248234a2a9c4dffdc9a5769b48abb"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.4/opencapy_darwin_arm64.tar.gz"
      sha256 "5ab7cc58ccfe74b44427afdfd37bd08f2720abcb6b555e542ee3ed7d2f469761"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.4/opencapy_linux_amd64.tar.gz"
      sha256 "8153d7cd900220fa9e64812eccaa8aa532d2eefe1cde3111139c6395de0bfadd"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.4/opencapy_linux_arm64.tar.gz"
      sha256 "db41b16ce25cfeeabe35115f277f716fc02ab03f2d9a6d69f00ac93dc0088d12"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
