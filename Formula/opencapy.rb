# typed: false
# frozen_string_literal: true

class Opencapy < Formula
  desc "Your machines, mirrored. Code from anywhere."
  homepage "https://opencapy.dev"
  version "0.2.15"
  license "MIT"

  on_macos do
    if Hardware::CPU.intel?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.15/opencapy_darwin_amd64.tar.gz"
      sha256 "2fa4e3bea2383079f756640081120281a93e099f3fb5d7c1a9cae239db2cc2f0"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.15/opencapy_darwin_arm64.tar.gz"
      sha256 "8e907c6089bcc93ed0c7d4ed5f1ecf9dc11c711c7e8b96b3e944543c6f161a25"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  on_linux do
    if Hardware::CPU.intel? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.15/opencapy_linux_amd64.tar.gz"
      sha256 "224c26c637fc9de298af5a00825c260c7f8dbc58983c4b2119b1339519bd5507"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
    if Hardware::CPU.arm? && Hardware::CPU.is_64_bit?
      url "https://github.com/richardyc/opencapy/releases/download/v0.2.15/opencapy_linux_arm64.tar.gz"
      sha256 "bdace9d864771c5a43c89d153d42064709d215123c9ea6a4fe6109adc9b59f1e"

      define_method(:install) do
        bin.install "opencapy"
      end
    end
  end

  test do
    system "#{bin}/opencapy", "--help"
  end
end
