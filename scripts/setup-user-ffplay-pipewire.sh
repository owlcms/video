#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FFMPEG_SCRIPT="$SCRIPT_DIR/install-ffmpeg-nvenc.sh"
PLUGIN_RELEASE_API="https://api.github.com/repos/dimtpap/obs-pipewire-audio-capture/releases/latest"

ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"

echo "=== User Media Setup (FFplay + OBS PipeWire Plugin) ==="
echo "Detected architecture: $ARCH"

if [ ! -x "$FFMPEG_SCRIPT" ]; then
    chmod +x "$FFMPEG_SCRIPT"
fi

echo
echo "Step 1/4: Install OBS and base tools..."
sudo apt-get update
sudo apt-get install -y software-properties-common curl tar obs-studio pulseaudio-utils v4l-utils vlc

echo
echo "Step 2/4: Install FFmpeg/FFplay via project script (Jellyfin FFmpeg 7 + ffplay on PATH)..."
sudo "$FFMPEG_SCRIPT"

echo
echo "Step 3/4: Download OBS PipeWire plugin from GitHub releases..."
if [ "$ARCH" != "amd64" ]; then
    echo "⚠ Skipping OBS PipeWire plugin: no pre-built package available for $ARCH."
    echo "  Install manually from: https://github.com/dimtpap/obs-pipewire-audio-capture/releases"
else
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
fi

echo
echo "Step 4/4: Verify installs..."
ffmpeg -version | head -1
ffplay -version | head -1
vlc --version | head -1

if ffmpeg -hide_banner -encoders 2>&1 | grep -q h264_nvenc; then
    echo "✓ NVENC encoder is available"
else
    echo "⚠ NVENC encoder not found (check NVIDIA driver/runtime)"
fi

if [ "$ARCH" = "amd64" ]; then
    if find "$HOME/.config/obs-studio/plugins" -type f | grep -qi 'pipewire'; then
        echo "✓ OBS PipeWire plugin files detected in ~/.config/obs-studio/plugins"
    else
        echo "⚠ Could not confirm plugin files. Restart OBS and check Sources list."
    fi
else
    echo "ℹ OBS PipeWire plugin check skipped (not installed on $ARCH)."
fi

echo
echo "Setup complete. Restart OBS and add 'Application Audio Capture (PipeWire)'."
