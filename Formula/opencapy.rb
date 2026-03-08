# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.3"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.3/opencapy_darwin_amd64.tar.gz"
      sha256 "9111f48c77274d40982a1f9499c0e4b9703efa3db196861b8300bc8517eded31"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.3/opencapy_darwin_arm64.tar.gz"
      sha256 "6502129418e026eb6d440cbf766ee449bd940c16d8ccaf694fc3ee0aa01b7842"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.3/opencapy_linux_amd64.tar.gz"
      sha256 "21cf7b86af119cd04627475880cfc1c2b3ac1999ccffd0b7f109fa429a04bcfe"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.3/opencapy_linux_arm64.tar.gz"
      sha256 "f66a1c18c4bb4fdd79d5c9004f700d629c824dd30336ddcb937e400f9f8372c2"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
