#!/usr/bin/env bash
set -euo pipefail

REPO="richardyc/opencapy"
INSTALL_DIR="/usr/local/bin"

# Detect OS
OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
case "$OS" in
  linux)  OS="linux" ;;
  darwin) OS="darwin" ;;
  *)
    echo "Unsupported OS: $OS"
    exit 1
    ;;
esac

# Detect architecture
ARCH="$(uname -m)"
case "$ARCH" in
  x86_64|amd64)  ARCH="amd64" ;;
  arm64|aarch64) ARCH="arm64" ;;
  *)
    echo "Unsupported architecture: $ARCH"
    exit 1
    ;;
esac

echo "Detected: ${OS}/${ARCH}"

# Fetch latest release tag
echo "Fetching latest release..."
TAG=$(curl -sL "https://api.github.com/repos/${REPO}/releases/latest" | grep '"tag_name"' | head -1 | cut -d'"' -f4)

if [ -z "$TAG" ]; then
  echo "Error: Could not determine latest release."
  exit 1
fi

echo "Latest release: ${TAG}"

# Download
TARBALL="opencapy_${OS}_${ARCH}.tar.gz"
URL="https://github.com/${REPO}/releases/download/${TAG}/${TARBALL}"

echo "Downloading ${URL}..."
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

curl -sL "$URL" -o "${TMP}/${TARBALL}"

# Extract
echo "Extracting..."
tar -xzf "${TMP}/${TARBALL}" -C "$TMP"

# Install
echo "Installing to ${INSTALL_DIR}/opencapy..."
if [ -w "$INSTALL_DIR" ]; then
  cp "${TMP}/opencapy" "${INSTALL_DIR}/opencapy"
else
  sudo cp "${TMP}/opencapy" "${INSTALL_DIR}/opencapy"
fi
chmod +x "${INSTALL_DIR}/opencapy"

echo ""
echo "opencapy ${TAG} installed successfully!"
echo ""
echo "Next steps:"
echo "  1. Run: opencapy install    # Set up auto-start daemon"
echo "  2. Run: opencapy            # Create your first session"
echo "  3. Run: opencapy qr         # Pair with iOS app"
