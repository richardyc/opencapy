# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.17"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.17/opencapy_darwin_amd64.tar.gz"
      sha256 "d3d2264ac751b52d9d09b52b2a403b44cc355b715c67a96679c0a1ec710dc389"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.17/opencapy_darwin_arm64.tar.gz"
      sha256 "7b77901970880ff8a43d599245c6d98e04fe58563f46e9783418f3c50771af74"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.17/opencapy_linux_amd64.tar.gz"
      sha256 "136376b93b3cb66cff03c791dc6a07039f945c9e512139d19382a65493055a10"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.17/opencapy_linux_arm64.tar.gz"
      sha256 "ba67184be76e324f77d9e21d317866e3b50d20c66b6f63dc3466d6f053d9c083"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
