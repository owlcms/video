#!/usr/bin/env bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
MEDIAMTX_EXE="$SCRIPT_DIR/mediamtx.exe"
STREAM_NAME="${STREAM_NAME:-uvc}"
RTSP_URL="${RTSP_URL:-rtsp://127.0.0.1:8554/${STREAM_NAME}}"
CAMERA_NAME="${CAMERA_NAME:-UVC Camera}"
VIDEO_SIZE="${VIDEO_SIZE:-1920x1080}"
FRAME_RATE="${FRAME_RATE:-60}"
PIXEL_FORMAT="${PIXEL_FORMAT:-nv12}"
GOP_SIZE="${GOP_SIZE:-60}"
NVENC_PRESET="${NVENC_PRESET:-p4}"
RTBUF_SIZE="${RTBUF_SIZE:-256M}"
STOP_EXISTING_MEDIAMTX="${STOP_EXISTING_MEDIAMTX:-1}"

if ! command -v ffmpeg >/dev/null 2>&1; then
    echo "ffmpeg is not on PATH" >&2
    exit 1
fi

if [ ! -x "$MEDIAMTX_EXE" ]; then
    echo "MediaMTX executable not found at $MEDIAMTX_EXE" >&2
    echo "On Linux, use scripts/start-uvc-rtsp-linux.sh instead." >&2
    exit 1
fi

mediamtx_pid=""
mediamtx_config=""

cleanup() {
    if [ -n "$mediamtx_pid" ] && kill -0 "$mediamtx_pid" 2>/dev/null; then
        kill "$mediamtx_pid" 2>/dev/null || true
        wait "$mediamtx_pid" 2>/dev/null || true
    fi
    if [ -n "$mediamtx_config" ] && [ -f "$mediamtx_config" ]; then
        rm -f "$mediamtx_config"
    fi
}

trap cleanup EXIT INT TERM

mediamtx_config="$(mktemp)"
cat > "$mediamtx_config" <<EOF
paths:
    ${STREAM_NAME}:
        source: publisher
EOF

if [ "$STOP_EXISTING_MEDIAMTX" = "1" ]; then
        taskkill.exe //IM mediamtx.exe //F >/dev/null 2>&1 || true
fi

echo "Starting MediaMTX from $MEDIAMTX_EXE"
"$MEDIAMTX_EXE" "$mediamtx_config" &
mediamtx_pid=$!

sleep 1

echo "Publishing $CAMERA_NAME to $RTSP_URL"
exec ffmpeg \
    -hide_banner \
    -loglevel info \
    -f dshow \
    -rtbufsize "$RTBUF_SIZE" \
    -use_video_device_timestamps false \
    -use_wallclock_as_timestamps 1 \
    -fflags +genpts \
    -video_size "$VIDEO_SIZE" \
    -framerate "$FRAME_RATE" \
    -pixel_format "$PIXEL_FORMAT" \
    -i video="$CAMERA_NAME" \
    -an \
    -c:v h264_nvenc \
    -preset "$NVENC_PRESET" \
    -tune ll \
    -pix_fmt yuv420p \
    -g "$GOP_SIZE" \
    -f rtsp \
    -rtsp_transport tcp \
    "$RTSP_URL"