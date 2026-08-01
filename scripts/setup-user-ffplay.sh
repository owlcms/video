#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FFMPEG_SCRIPT="$SCRIPT_DIR/install-ffmpeg-nvenc.sh"

echo "=== User FFplay Setup (FFmpeg + FFplay) ==="

if [ ! -x "$FFMPEG_SCRIPT" ]; then
    chmod +x "$FFMPEG_SCRIPT"
fi

echo
echo "Step 1/3: Install base tools..."
sudo apt-get update
sudo apt-get install -y software-properties-common

echo
echo "Step 2/3: Install FFmpeg/FFplay via project script..."
sudo "$FFMPEG_SCRIPT"

echo
echo "Step 3/3: Verify installs..."
ffmpeg -version | head -1
ffplay -version | head -1

if ffmpeg -hide_banner -encoders 2>&1 | grep -q h264_nvenc; then
    echo "✓ NVENC encoder is available"
else
    echo "⚠ NVENC encoder not found (check NVIDIA driver/runtime)"
fi

echo
echo "FFplay setup complete."
