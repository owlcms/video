#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
package_dir="$repo_root"

cd "$package_dir"
go run fyne.io/fyne/v2/cmd/fyne@latest package \
  -os darwin \
  -name Video \
  -appID app.owlcms.video \
  -icon "$repo_root/internal/assets/Icon.png"

app_bundle="$package_dir/Video.app"
info_plist="$app_bundle/Contents/Info.plist"
plutil -insert NSCameraUsageDescription \
  -string 'OWLCMS Video uses connected cameras to capture competition video.' \
  "$info_plist"
codesign --force --deep --sign - "$app_bundle"

printf 'Created %s\n' "$package_dir/Video.app"
