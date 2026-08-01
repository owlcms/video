#!/usr/bin/env bash
# Build and run the development Video binary with repository-local configuration.

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
binary="$repo_root/video"
config_dir="${VIDEO_DEV_CONFIG_DIR:-$repo_root/video_config}"

cd "$repo_root"
echo "Building development Video binary..."
go build -o "$binary" .

if [[ ! -f "$config_dir/cameras.toml" || ! -f "$config_dir/replays.toml" || ! -f "$config_dir/ffmpeg.toml" ]]; then
	echo "Creating missing development configuration files in $config_dir"
	"$binary" --configDir "$config_dir" --extractConfig
fi

exec "$binary" --configDir "$config_dir" "$@"