#!/bin/bash

set -euo pipefail

PLUGIN_RELEASE_API="https://api.github.com/repos/dimtpap/obs-pipewire-audio-capture/releases/latest"

echo "=== User OBS Setup ==="

echo
echo "Step 1/3: Install OBS and audio tools..."
sudo apt-get update
sudo apt-get install -y curl tar obs-studio pulseaudio-utils

echo
echo "Step 2/3: Download OBS audio capture plugin from GitHub releases..."
ASSET_URL="$(curl -fsSL "$PLUGIN_RELEASE_API" | grep -oE 'https://[^\"]*linux-pipewire-audio[^\"]*\.tar\.gz' | head -n1)"

if [ -z "$ASSET_URL" ]; then
    echo "Could not determine latest plugin tarball from: $PLUGIN_RELEASE_API"
    echo "Install manually from: https://github.com/dimtpap/obs-pipewire-audio-capture/releases"
    exit 1
fi

TMPDIR="$(mktemp -d)"
trap 'rm -rf "$TMPDIR"' EXIT

PLUGIN_TGZ="$TMPDIR/linux-pipewire-audio.tar.gz"
curl -fL "$ASSET_URL" -o "$PLUGIN_TGZ"

mkdir -p "$HOME/.config/obs-studio/plugins"
tar -xzf "$PLUGIN_TGZ" -C "$HOME/.config/obs-studio/plugins"

echo
echo "Step 3/3: Verify installs..."
if pactl info | grep -q "Server Name"; then
    pactl info | grep "Server Name"
else
    echo "⚠ Could not read audio server name via pactl"
fi

if find "$HOME/.config/obs-studio/plugins" -type f | grep -qi 'pipewire'; then
    echo "✓ OBS audio plugin files detected in ~/.config/obs-studio/plugins"
else
    echo "⚠ Could not confirm plugin files. Restart OBS and check Sources list."
fi

echo
echo "OBS setup complete. Restart OBS and add 'Application Audio Capture (PipeWire)'."
