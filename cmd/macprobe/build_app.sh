#!/bin/bash
# macprobe を MacProbe.app にまとめて ad-hoc 署名する（macOS 上で実行する）。
# 画面収録・アクセシビリティの許可は「アプリ」単位で付けるため、ssh 経由の素のバイナリではなく .app を open で起動して測る。
#   usage: build_app.sh [出力先ディレクトリ（既定 ./）] [BUILD_TAG]
# BUILD_TAG を変えて同じ場所に作り直すと、ad-hoc 署名の cdhash が変わる（許可が外れるかの確認用）。
set -euo pipefail
cd "$(dirname "$0")/../.."
dest="$(cd "${1:-.}" && pwd)"
tag="${2:-}"
app="$dest/MacProbe.app"
mkdir -p "$app/Contents/MacOS"
go build -ldflags "-X main.buildTag=$tag" -o "$app/Contents/MacOS/macprobe" ./cmd/macprobe
cat >"$app/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundleIdentifier</key><string>com.ousiass.puppet-remo.macprobe</string>
	<key>CFBundleName</key><string>MacProbe</string>
	<key>CFBundleDisplayName</key><string>MacProbe</string>
	<key>CFBundleExecutable</key><string>macprobe</string>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleVersion</key><string>1</string>
	<key>CFBundleShortVersionString</key><string>1</string>
	<key>LSUIElement</key><true/>
	<key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST
codesign --force --sign - "$app"
codesign -dv "$app" 2>&1 | grep -E 'Identifier|CodeDirectory|Signature|CDHash' || true
echo "$app"
