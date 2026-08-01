#!/usr/bin/env bash

set -euo pipefail

STREAM_NAME="${STREAM_NAME:-uvc}"
RTSP_URL="${RTSP_URL:-rtsp://127.0.0.1:8554/${STREAM_NAME}}"
CAMERA_DEVICE="${CAMERA_DEVICE:-/dev/video0}"
VIDEO_SIZE="${VIDEO_SIZE:-1920x1080}"
FRAME_RATE="${FRAME_RATE:-60}"
INPUT_FORMAT="${INPUT_FORMAT:-}"
GOP_SIZE="${GOP_SIZE:-60}"
NVENC_PRESET="${NVENC_PRESET:-p5}"
BITRATE="${BITRATE:-8M}"
THREAD_QUEUE_SIZE="${THREAD_QUEUE_SIZE:-4096}"
RTBUF_SIZE="${RTBUF_SIZE:-512M}"
STOP_EXISTING_MEDIAMTX="${STOP_EXISTING_MEDIAMTX:-1}"

if ! command -v ffmpeg >/dev/null 2>&1; then
    echo "ffmpeg is not on PATH" >&2
    exit 1
fi

if ! command -v mediamtx >/dev/null 2>&1; then
    echo "mediamtx is not on PATH" >&2
    exit 1
fi

if [ ! -e "$CAMERA_DEVICE" ]; then
    echo "Camera device not found at $CAMERA_DEVICE" >&2
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
    pkill -x mediamtx >/dev/null 2>&1 || true
fi

echo "Starting MediaMTX"
mediamtx "$mediamtx_config" &
mediamtx_pid=$!

sleep 1

input_args=(
    -f v4l2
    -rtbufsize "$RTBUF_SIZE"
    -thread_queue_size "$THREAD_QUEUE_SIZE"
    -framerate "$FRAME_RATE"
    -video_size "$VIDEO_SIZE"
)

if [ -n "$INPUT_FORMAT" ]; then
    input_args+=( -input_format "$INPUT_FORMAT" )
fi

echo "Publishing $CAMERA_DEVICE to $RTSP_URL"
exec ffmpeg \
    -hide_banner \
    -loglevel info \
    "${input_args[@]}" \
    -i "$CAMERA_DEVICE" \
    -an \
    -c:v h264_nvenc \
    -preset "$NVENC_PRESET" \
    -rc cbr \
    -b:v "$BITRATE" \
    -g "$GOP_SIZE" \
    -keyint_min "$GOP_SIZE" \
    -r "$FRAME_RATE" \
    -vsync cfr \
    -f rtsp \
    -rtsp_transport tcp \
    "$RTSP_URL"