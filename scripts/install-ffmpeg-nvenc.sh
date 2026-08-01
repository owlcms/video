#!/bin/bash
# Install Jellyfin's ffmpeg build with NVENC support
# Keep distro ffmpeg installed first so ffplay is always available
#
# Run as: sudo ./install-ffmpeg-nvenc.sh

set -e

FFMPEG_URL="https://repo.jellyfin.org/files/ffmpeg/ubuntu/latest-7.x/amd64"

# Detect Ubuntu version
if [ -f /etc/os-release ]; then
    . /etc/os-release
    UBUNTU_CODENAME="${VERSION_CODENAME:-noble}"
else
    UBUNTU_CODENAME="noble"
fi

ARCH="$(dpkg --print-architecture 2>/dev/null || uname -m)"

echo "=== Jellyfin FFmpeg Installer (with NVENC) ==="
echo "Detected Ubuntu codename: $UBUNTU_CODENAME"
echo "Detected architecture: $ARCH"
echo

# Check for root
if [ "$EUID" -ne 0 ]; then
    echo "Please run as root: sudo $0"
    exit 1
fi

# Jellyfin only ships amd64 packages; on arm64 or any other arch use distro ffmpeg only
if [ "$ARCH" != "amd64" ]; then
    echo "Architecture $ARCH is not amd64 — skipping Jellyfin, installing distro ffmpeg/ffplay only."
    apt-get update
    apt-get install -y ffmpeg
    echo
    echo "=== Verification ==="
    ffmpeg -version 2>&1 | head -1
    ffplay -version 2>&1 | head -1
    echo
    echo "=== Installation complete ==="
    exit 0
fi

# Step 1: Remove ubuntuhandbook1 PPA if present
echo "Checking for ubuntuhandbook1 ffmpeg PPA..."
if grep -rq "ubuntuhandbook1/ffmpeg" /etc/apt/sources.list.d/ 2>/dev/null; then
    echo "Removing ubuntuhandbook1 ffmpeg PPA..."
    add-apt-repository -y --remove ppa:ubuntuhandbook1/ffmpeg7 2>/dev/null || true
    add-apt-repository -y --remove ppa:ubuntuhandbook1/ffmpeg6 2>/dev/null || true
    apt-get update
fi

# Step 2: Ensure distro ffmpeg/ffplay is installed first
echo "Installing distro ffmpeg/ffplay..."
apt-get update
apt-get install -y ffmpeg

# Step 3: Download Jellyfin ffmpeg
echo "Downloading Jellyfin ffmpeg 7.x for $UBUNTU_CODENAME..."
TMPDIR=$(mktemp -d)
cd "$TMPDIR"

# Use noble (24.04) or jammy (22.04) based on detected version
case "$UBUNTU_CODENAME" in
    noble|oracular|plucky)
        DEB_NAME="jellyfin-ffmpeg7_7.1.3-3-noble_amd64.deb"
        ;;
    jammy|kinetic|lunar|mantic)
        DEB_NAME="jellyfin-ffmpeg7_7.1.3-3-jammy_amd64.deb"
        ;;
    *)
        echo "Warning: Unknown Ubuntu version, using noble package"
        DEB_NAME="jellyfin-ffmpeg7_7.1.3-3-noble_amd64.deb"
        ;;
esac

wget -q --show-progress "${FFMPEG_URL}/${DEB_NAME}"

# Step 4: Install Jellyfin ffmpeg
echo "Installing Jellyfin ffmpeg..."
dpkg -i "$DEB_NAME" || apt-get -f install -y

# Step 5: Restore distro ffplay after Jellyfin install (if package changes removed it)
echo "Ensuring distro ffplay is present..."
apt-get install -y ffmpeg

# Step 6: Create symlinks to make Jellyfin the default for ffmpeg/ffprobe
echo "Creating symlinks..."
JELLYFIN_PATH="/usr/lib/jellyfin-ffmpeg"

for bin in ffmpeg ffprobe; do
    if [ -f "$JELLYFIN_PATH/$bin" ]; then
        # Backup existing if it's a real file (not symlink)
        if [ -f "/usr/bin/$bin" ] && [ ! -L "/usr/bin/$bin" ]; then
            mv "/usr/bin/$bin" "/usr/bin/${bin}.ubuntu-backup"
        fi
        ln -sf "$JELLYFIN_PATH/$bin" "/usr/bin/$bin"
        echo "  /usr/bin/$bin -> $JELLYFIN_PATH/$bin"
    fi
done

if [ -f "$JELLYFIN_PATH/ffplay" ]; then
    if [ -f "/usr/bin/ffplay" ] && [ ! -L "/usr/bin/ffplay" ]; then
        mv "/usr/bin/ffplay" "/usr/bin/ffplay.ubuntu-backup"
    fi
    ln -sf "$JELLYFIN_PATH/ffplay" "/usr/bin/ffplay"
    echo "  /usr/bin/ffplay -> $JELLYFIN_PATH/ffplay"
else
    echo "  Keeping distro /usr/bin/ffplay (Jellyfin package has no ffplay binary)"
fi

# Cleanup
rm -rf "$TMPDIR"

# Step 7: Verify installation
echo
echo "=== Verification ==="
if ! command -v ffplay >/dev/null 2>&1; then
    echo "ERROR: ffplay is not on PATH after installation"
    exit 1
fi

echo "FFmpeg version:"
ffmpeg -version 2>&1 | head -1
echo "FFplay version:"
ffplay -version 2>&1 | head -1
echo
echo "NVENC support:"
if ffmpeg -hide_banner -encoders 2>&1 | grep -q h264_nvenc; then
    echo "  ✓ h264_nvenc available"
else
    echo "  ✗ h264_nvenc NOT available (check NVIDIA drivers)"
fi
echo
echo "VAAPI support:"
if ffmpeg -hide_banner -encoders 2>&1 | grep -q h264_vaapi; then
    echo "  ✓ h264_vaapi available"
else
    echo "  ✗ h264_vaapi NOT available"
fi
echo
echo "QSV support:"
if ffmpeg -hide_banner -encoders 2>&1 | grep -q h264_qsv; then
    echo "  ✓ h264_qsv available"
else
    echo "  ✗ h264_qsv NOT available"
fi

echo
echo "=== Installation complete ==="

