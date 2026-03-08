# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.6"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.6/opencapy_darwin_amd64.tar.gz"
      sha256 "eaf3a327af711bf08300f7211f0af170369206de88a1b900c6acc4f7ae3d5869"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.6/opencapy_darwin_arm64.tar.gz"
      sha256 "5102fc338945eb92a68b0f9b57b99811de97badee468a62c106573e5f93e5ac2"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.6/opencapy_linux_amd64.tar.gz"
      sha256 "a05f23a19d084913b5f6584360bbf87e41c2d4f60352c6d92c736139259aa102"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.6/opencapy_linux_arm64.tar.gz"
      sha256 "a4f93fcc2999acdbf661afe22544e75baa95e1a45aab3aac8207ee262966a7f1"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
