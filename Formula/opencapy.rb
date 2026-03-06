# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.0"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.0/opencapy_darwin_amd64.tar.gz"
      sha256 "276b3add37d6796205ba028e2f525ddfcaa77dcdf3f63cb0317cd26e1af14261"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.0/opencapy_darwin_arm64.tar.gz"
      sha256 "4f83994710809198b70dc769c3091c8b5197c0f9bab9f0fc80a1b0f1d6c12595"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.0/opencapy_linux_amd64.tar.gz"
      sha256 "f5dd7e850da25abab88da72a0466e3426c1d69e7dd817e5f25c9862d15e66769"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.0/opencapy_linux_arm64.tar.gz"
      sha256 "bbcd95532e1d6da0a8c7dc54cafca86ab477028f90b95286bf87caa965e2739f"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
