#!/usr/bin/env bash
# Run the newest installed Video version, including prereleases, with that
# version's configuration.

set -euo pipefail

case "$(uname -s)" in
	Darwin)
		default_install_dir="$HOME/Library/Application Support/owlcms-video"
		case "$(uname -m)" in
			x86_64) binary_name="video_darwin_amd64" ;;
			arm64) binary_name="video_darwin_arm64" ;;
			*) echo "Unsupported macOS architecture: $(uname -m)" >&2; exit 1 ;;
		esac
		;;
	Linux)
		default_install_dir="$HOME/.local/share/owlcms-video"
		case "$(uname -m)" in
			x86_64) binary_name="video_linux_amd64" ;;
			aarch64|arm64) binary_name="video_linux_arm64" ;;
			*) echo "Unsupported Linux architecture: $(uname -m)" >&2; exit 1 ;;
		esac
		;;
	MINGW*|MSYS*|CYGWIN*)
		default_install_dir="${APPDATA:?APPDATA is required to locate the installed Video release}/owlcms-video"
		binary_name="video_windows.exe"
		;;
	*)
		echo "Unsupported operating system: $(uname -s)" >&2
		exit 1
		;;
esac

install_dir="${VIDEO_INSTALL_DIR:-$default_install_dir}"

version_is_newer() {
	local candidate="${1#v}"
	local current="${2#v}"
	local candidate_base="${candidate%%-*}"
	local current_base="${current%%-*}"
	local candidate_pre="${candidate#"$candidate_base"}"
	local current_pre="${current#"$current_base"}"
	local -a candidate_parts current_parts
	local index candidate_part current_part

	IFS='.' read -r -a candidate_parts <<< "$candidate_base"
	IFS='.' read -r -a current_parts <<< "$current_base"
	for index in 0 1 2; do
		candidate_part="${candidate_parts[index]:-0}"
		current_part="${current_parts[index]:-0}"
		[[ "$candidate_part" =~ ^[0-9]+$ ]] || candidate_part=0
		[[ "$current_part" =~ ^[0-9]+$ ]] || current_part=0
		if ((10#$candidate_part != 10#$current_part)); then
			((10#$candidate_part > 10#$current_part))
			return
		fi
	done

	if [[ -z "$candidate_pre" && -n "$current_pre" ]]; then
		return 0
	fi
	if [[ -n "$candidate_pre" && -z "$current_pre" ]]; then
		return 1
	fi
	[[ "$candidate_pre" > "$current_pre" ]]
}

latest_version=""
for version_dir in "$install_dir"/*; do
	[[ -d "$version_dir" ]] || continue
	if [[ ! -f "$version_dir/cameras.toml" || ! -f "$version_dir/replays.toml" || ! -f "$version_dir/ffmpeg.toml" ]]; then
		continue
	fi
	version="$(basename "$version_dir")"
	if [[ -z "$latest_version" ]] || version_is_newer "$version" "$latest_version"; then
		latest_version="$version"
	fi
done

if [[ -z "$latest_version" ]]; then
	echo "No installed Video version with cameras.toml, replays.toml, and ffmpeg.toml was found in $install_dir" >&2
	echo "Set VIDEO_INSTALL_DIR to the Video installation root if it is elsewhere." >&2
	exit 1
fi

version_dir="$install_dir/$latest_version"
binary="$version_dir/$binary_name"
if [[ ! -f "$binary" ]]; then
	echo "Video binary not found for version $latest_version: $binary" >&2
	exit 1
fi

if [[ "$(uname -s)" != MINGW* && "$(uname -s)" != MSYS* && "$(uname -s)" != CYGWIN* ]]; then
	chmod u+x "$binary"
fi

echo "Starting Video $latest_version with configuration in $version_dir"
cd "$version_dir"
exec "$binary" --configDir "$version_dir" "$@"