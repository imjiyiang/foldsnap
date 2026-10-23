#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/../.."
output="${1:-release}"
app="$output/Foldsnap.app"
python3 - "$app" <<'PYGUARD'
import pathlib,subprocess,sys
prefix=str(pathlib.Path(sys.argv[1]).resolve())+'/Contents/MacOS/'
for row in subprocess.check_output(['ps','-axo','pid=,command='],text=True).splitlines():
    parts=row.strip().split(maxsplit=1)
    if len(parts)==2 and parts[1].startswith(prefix):
        raise SystemExit('目标应用正在运行；请退出后打包，或指定独立输出目录。')
PYGUARD
mkdir -p "$output"
stage="$(mktemp -d "$output/.LocalBackup-build.XXXXXX")"
trap 'rm -rf "$stage"' EXIT
final_app="$app"
app="$stage/Foldsnap.app"
mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
cp bin/localbackup "$app/Contents/MacOS/localbackup-service"
swiftc -O packaging/macos/Launcher.swift -o "$app/Contents/MacOS/LocalBackup" -framework AppKit
if [ "$app/Contents/MacOS/LocalBackup" -ef "$app/Contents/MacOS/localbackup-service" ]; then
  echo 'Launcher and backend must be distinct files on case-insensitive macOS volumes.' >&2
  exit 1
fi
"$app/Contents/MacOS/localbackup-service" -h >/dev/null 2>&1
cat > "$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>CFBundleName</key><string>Foldsnap</string>
<key>CFBundleDisplayName</key><string>Foldsnap · 留档</string>
<key>CFBundleIdentifier</key><string>app.localbackup.mac</string>
<key>CFBundleVersion</key><string>0.22.0</string>
<key>CFBundleShortVersionString</key><string>0.22.0</string>
<key>CFBundleExecutable</key><string>LocalBackup</string>
<key>CFBundleIconFile</key><string>AppIcon.icns</string>
<key>CFBundlePackageType</key><string>APPL</string>
<key>LSUIElement</key><true/>
<key>NSHighResolutionCapable</key><true/>
<key>NSDocumentsFolderUsageDescription</key><string>备份你选择的文档目录。</string>
<key>NSDesktopFolderUsageDescription</key><string>备份你选择的桌面目录。</string>
<key>NSDownloadsFolderUsageDescription</key><string>备份你选择的下载目录。</string>
<key>NSRemovableVolumesUsageDescription</key><string>将你选择的目录备份到外置磁盘。</string>
</dict></plist>
PLIST
iconset="$stage/AppIcon.iconset"
mkdir -p "$iconset"
for size in 16 32 128 256 512; do
  sips -z "$size" "$size" assets/brand/app-icon.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
  retina=$((size * 2))
  sips -z "$retina" "$retina" assets/brand/app-icon.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil --convert icns --output "$app/Contents/Resources/AppIcon.icns" "$iconset"
cp assets/brand/status-icon.png "$app/Contents/Resources/StatusIcon.png"
cp LICENSE "$app/Contents/Resources/LICENSE"
cp THIRD_PARTY_NOTICES.md "$app/Contents/Resources/THIRD_PARTY_NOTICES.md"
codesign --force --deep --sign - "$app"
codesign --verify --deep --strict "$app"
ditto -c -k --sequesterRsrc --keepParent "$app" "$stage/Foldsnap-macOS.zip"
if [ -e "$final_app" ]; then mv "$final_app" "$stage/previous.app"; fi
if ! mv "$app" "$final_app"; then
  if [ -e "$stage/previous.app" ]; then mv "$stage/previous.app" "$final_app"; fi
  exit 1
fi
mv "$stage/Foldsnap-macOS.zip" "$output/Foldsnap-macOS.zip"
app="$final_app"
printf 'Built %s\n' "$app"
