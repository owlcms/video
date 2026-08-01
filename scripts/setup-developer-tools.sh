#!/bin/bash

set -euo pipefail

GO_VERSION="${GO_VERSION:-1.26.0}"

echo "=== Developer Tooling Setup ==="

echo
echo "Step 1/5: Install build and graphics dependencies..."
sudo apt-get update
sudo apt-get install -y \
    build-essential \
    pkg-config \
    libgl1-mesa-dev \
    libwayland-dev \
    libxkbcommon-dev \
    xorg-dev \
    git \
    curl \
    wget \
    tar \
    ca-certificates \
    software-properties-common

echo
echo "Step 2/5: Install Go from official tarball..."
ARCH="$(dpkg --print-architecture)"
if [ "$ARCH" != "amd64" ]; then
    echo "This script currently supports amd64 Go tarballs. Detected: $ARCH"
    exit 1
fi

GO_TARBALL="go${GO_VERSION}.linux-amd64.tar.gz"
wget -q "https://go.dev/dl/${GO_TARBALL}"
sudo rm -rf /usr/local/go
sudo tar -C /usr/local -xzf "$GO_TARBALL"
rm -f "$GO_TARBALL"

if ! grep -q '/usr/local/go/bin' "$HOME/.bashrc"; then
    echo 'export PATH=$PATH:/usr/local/go/bin' >> "$HOME/.bashrc"
fi

export PATH="$PATH:/usr/local/go/bin"

echo
echo "Step 3/5: Ensure CGO is enabled for local builds..."
go env -w CGO_ENABLED=1

echo
echo "Step 4/5: Install VS Code..."
wget -qO- https://packages.microsoft.com/keys/microsoft.asc | gpg --dearmor > /tmp/packages.microsoft.gpg
sudo install -D -o root -g root -m 644 /tmp/packages.microsoft.gpg /etc/apt/keyrings/packages.microsoft.gpg
echo "deb [arch=amd64 signed-by=/etc/apt/keyrings/packages.microsoft.gpg] https://packages.microsoft.com/repos/code stable main" | sudo tee /etc/apt/sources.list.d/vscode.list > /dev/null
rm -f /tmp/packages.microsoft.gpg
sudo apt-get update
sudo apt-get install -y code

echo
echo "Step 5/5: Install GitHub CLI (gh)..."
curl -fsSL https://cli.github.com/packages/githubcli-archive-keyring.gpg | sudo tee /usr/share/keyrings/githubcli-archive-keyring.gpg > /dev/null
sudo chmod go+r /usr/share/keyrings/githubcli-archive-keyring.gpg
echo "deb [arch=$(dpkg --print-architecture) signed-by=/usr/share/keyrings/githubcli-archive-keyring.gpg] https://cli.github.com/packages stable main" | sudo tee /etc/apt/sources.list.d/github-cli.list > /dev/null
sudo apt-get update
sudo apt-get install -y gh

echo
echo "Step 6/6: Verify toolchain..."
gcc --version | head -1
go version
go env CGO_ENABLED
gh --version | head -1
code --version | head -1

echo
echo "Developer tooling setup complete. Open a new shell before building."
