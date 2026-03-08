# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.7"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.7/opencapy_darwin_amd64.tar.gz"
      sha256 "54eaa99643ddd7aa155dcd53cd707ba480ffe0cc16857fee0161572b88dcbdff"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.7/opencapy_darwin_arm64.tar.gz"
      sha256 "60c6f7eaa37af78a218894ee7134c545eb1e40af2085327063f1066fc4ae7cf9"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.7/opencapy_linux_amd64.tar.gz"
      sha256 "b726b7dfddc8af257410eefcf5714ff3652eae8dcaada2398ff2925580b42de4"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.7/opencapy_linux_arm64.tar.gz"
      sha256 "b1d7a775a93a7ed03cacc0a3ef8e31041d46c31b065a1f2eb7484975bca27254"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
