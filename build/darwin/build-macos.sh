#!/bin/sh
# Builds YACM.app and a .dmg on a Mac (Apple silicon or Intel).
#
#   Requirements: Xcode command line tools (xcode-select --install),
#   Go 1.26 or newer (https://go.dev/dl/) and Node 20 or newer.
#
#   Usage, from the unpacked source folder:
#       sh build/darwin/build-macos.sh
#
# The result is dist-macos/YACM-<version>-macos.dmg: a universal app that
# runs on Apple silicon and Intel Macs with macOS 13 or newer (the oldest
# macOS the Go toolchain builds for), and the separate demo download
# dist-macos/yacm-demo-device-<version>-macos.zip (the demo device and demo.yacm).
#
# Signing:
#   MACOS_SIGN_IDENTITY  a "Developer ID Application: …" certificate in the
#                        keychain: the app, the disk image and the demo
#                        device are signed with it (hardened runtime, secure
#                        timestamp). Without it they are signed ad hoc: a copy
#                        built on your own Mac opens normally, a downloaded
#                        one only after System Settings > Privacy & Security
#                        > Open Anyway.
#   APPLE_API_KEY_PATH, APPLE_API_KEY_ID, APPLE_API_ISSUER_ID
#                        an App Store Connect API key (.p8 file): with these
#                        and a Developer ID the disk image and the demo zip
#                        are notarised by Apple and the ticket stapled to the
#                        disk image, so both open without any warning.
set -eu

cd "$(dirname "$0")/../.."
for tool in xcrun go npm lipo hdiutil codesign sips iconutil ditto; do
	command -v "$tool" >/dev/null 2>&1 || { echo "Missing: $tool (see the requirements at the top of this script)"; exit 1; }
done

VERSION=$(sed -n 's/^[[:space:]]*appVersion = "\(.*\)"/\1/p' core/version.go)
NUMVER=${VERSION%%-*} # bundle versions are numbers: 0.9.0-rc.1 -> 0.9.0
OUT=dist-macos
APP="$OUT/YACM.app"
STAGE="$OUT/stage"
DEMO="$OUT/yacm-demo-device-$VERSION-macos"
echo "Building YACM $VERSION for macOS"
rm -rf "$OUT"
mkdir -p "$APP/Contents/MacOS" "$APP/Contents/Resources" "$STAGE" "$DEMO"

echo "1/5 Frontend"
(cd frontend && npm ci --no-audit --no-fund && npm run build)

echo "2/5 App and demo device (arm64 + amd64)"
export CGO_ENABLED=1 MACOSX_DEPLOYMENT_TARGET=13.0
export CGO_CFLAGS="-mmacosx-version-min=13.0"
export CGO_LDFLAGS="-framework UniformTypeIdentifiers -mmacosx-version-min=13.0"
for arch in arm64 amd64; do
	GOOS=darwin GOARCH=$arch go build -trimpath -tags desktop,production -ldflags "-s -w" -o "$OUT/YACM-$arch" .
	GOOS=darwin GOARCH=$arch go build -trimpath -ldflags "-s -w" -o "$OUT/demo-$arch" ./cmd/yacm-demo-device
done
lipo -create -output "$APP/Contents/MacOS/YACM" "$OUT/YACM-arm64" "$OUT/YACM-amd64"
lipo -create -output "$DEMO/yacm-demo-device" "$OUT/demo-arm64" "$OUT/demo-amd64"

echo "3/5 App bundle"
ICONSET="$OUT/YACM.iconset"
mkdir -p "$ICONSET"
for size in 16 32 128 256 512; do
	sips -z $size $size build/appicon.png --out "$ICONSET/icon_${size}x${size}.png" >/dev/null
	double=$((size * 2))
	sips -z $double $double build/appicon.png --out "$ICONSET/icon_${size}x${size}@2x.png" >/dev/null
done
iconutil -c icns "$ICONSET" -o "$APP/Contents/Resources/YACM.icns"
cat >"$APP/Contents/Info.plist" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>CFBundlePackageType</key><string>APPL</string>
	<key>CFBundleName</key><string>YACM</string>
	<key>CFBundleDisplayName</key><string>YACM</string>
	<key>CFBundleExecutable</key><string>YACM</string>
	<key>CFBundleIdentifier</key><string>app.yacm.YACM</string>
	<key>CFBundleIconFile</key><string>YACM</string>
	<key>CFBundleShortVersionString</key><string>$NUMVER</string>
	<key>CFBundleVersion</key><string>$NUMVER</string>
	<key>LSMinimumSystemVersion</key><string>13.0</string>
	<key>NSHighResolutionCapable</key><true/>
	<key>NSHumanReadableCopyright</key><string>YACM $VERSION, GPL-3.0-or-later</string>
</dict>
</plist>
EOF
IDENTITY=${MACOS_SIGN_IDENTITY:-}
if [ -n "$IDENTITY" ]; then
	# Inside out: the executables first, then the bundle.
	sign() { codesign --force --options runtime --timestamp --sign "$IDENTITY" "$@"; }
	sign "$APP/Contents/MacOS/YACM"
	sign "$APP"
	sign "$DEMO/yacm-demo-device"
	codesign --verify --strict --verbose=2 "$APP"
else
	codesign --force --deep --sign - "$APP"
	codesign --force --sign - "$DEMO/yacm-demo-device"
fi

echo "4/5 Demo download"
go run ./tools/mkdemo "$DEMO/demo.yacm"
sed 's/yacm-demo-device\.exe/yacm-demo-device/g; s/Start it by double-clicking; close its window (or press Ctrl+C) to stop\./Start it in Terminal (.\/yacm-demo-device); press Ctrl+C to stop./' \
	build/demo/README-DEMO.txt >"$DEMO/README-DEMO.txt"
cp LICENSE NOTICE.md "$DEMO/"
(cd "$DEMO" && shasum -a 256 yacm-demo-device demo.yacm >SHA256SUMS.txt)
DEMOZIP="$OUT/yacm-demo-device-$VERSION-macos.zip"
ditto -c -k --keepParent "$DEMO" "$DEMOZIP"

echo "5/5 Disk image"
cp -R "$APP" "$STAGE/"
cp README.md LICENSE NOTICE.md "$STAGE/"
ln -s /Applications "$STAGE/Applications"
DMG="$OUT/YACM-$VERSION-macos.dmg"
hdiutil create -volname "YACM $VERSION" -srcfolder "$STAGE" -ov -format UDZO "$DMG" >/dev/null
# notarise FILE STAPLE: sends FILE to Apple; with STAPLE=1 the ticket is
# stapled to it (a disk image can hold one, a zip cannot: for the demo zip
# Gatekeeper finds the ticket online).
notarise() {
	KEY="--key $APPLE_API_KEY_PATH --key-id $APPLE_API_KEY_ID --issuer $APPLE_API_ISSUER_ID"
	# shellcheck disable=SC2086
	RESULT=$(xcrun notarytool submit "$1" $KEY --wait --timeout "${NOTARY_WAIT:-20m}" --output-format plist || true)
	STATUS=$(printf '%s' "$RESULT" | plutil -extract status raw - 2>/dev/null || echo unknown)
	SUBMISSION=$(printf '%s' "$RESULT" | plutil -extract id raw - 2>/dev/null || echo "")
	echo "   notarisation of $(basename "$1") $SUBMISSION: $STATUS"
	case "$STATUS" in
	Accepted)
		if [ "$2" = 1 ]; then
			xcrun stapler staple "$1"
			xcrun stapler validate "$1"
			spctl --assess --type open --context context:primary-signature --verbose=2 "$1"
		fi
		;;
	Invalid|Rejected)
		# shellcheck disable=SC2086
		[ -n "$SUBMISSION" ] && xcrun notarytool log "$SUBMISSION" $KEY || true
		echo "Apple refused $(basename "$1") (log above)."
		exit 1
		;;
	*)
		# Still in Apple's queue: the file is signed and keeps its
		# submission; once Apple accepts it, Gatekeeper finds the ticket
		# online, so it opens without a warning (not stapled).
		echo "::warning title=Notarisation pending::Apple has not finished notarising $(basename "$1") ($SUBMISSION) yet; it is signed but not stapled"
		;;
	esac
}
if [ -n "$IDENTITY" ]; then
	codesign --force --timestamp --sign "$IDENTITY" "$DMG"
	if [ -n "${APPLE_API_KEY_PATH:-}" ] && [ -n "${APPLE_API_KEY_ID:-}" ] && [ -n "${APPLE_API_ISSUER_ID:-}" ]; then
		echo "   notarising (usually a few minutes; the very first time can take hours)"
		notarise "$DMG" 1
		notarise "$DEMOZIP" 0
	else
		echo "   signed, not notarised (no App Store Connect API key)"
	fi
fi
rm -rf "$STAGE" "$DEMO" "$ICONSET" "$OUT"/YACM-arm64 "$OUT"/YACM-amd64 "$OUT"/demo-arm64 "$OUT"/demo-amd64

echo
echo "Done: $OUT/YACM-$VERSION-macos.dmg and $DEMOZIP"
echo "Open it and drag YACM to Applications."
