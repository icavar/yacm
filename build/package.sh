#!/usr/bin/env bash
# Builds the Windows and Linux release packages into dist/:
#   dist/yacm-<version>-windows-x64.zip        YACM.exe for x64
#   dist/yacm-<version>-windows-arm64.zip      YACM.exe for ARM
#   dist/yacm-<version>-linux-amd64.tar.gz     yacm
#   dist/yacm-<version>-demo-windows.zip       the demo device and demo.yacm
#   dist/yacm-<version>-demo-linux-amd64.tar.gz
# The demo is a separate download: YACM itself does not need it.
#
# Needs Go, Node and, for the Linux build, libgtk-3-dev and
# libwebkit2gtk-4.1-dev. The Windows executables are cross-compiled (no cgo),
# so this runs on Linux. Set SKIP_FRONTEND=1 if frontend/dist is already built.
# macOS has its own script: build/darwin/build-macos.sh (must run on a Mac).
set -euo pipefail

cd "$(dirname "$0")/.."
V=$(sed -n 's/^[[:space:]]*appVersion = "\(.*\)"/\1/p' core/version.go)
OUT=dist
WX=$OUT/yacm-$V-windows-x64
WA=$OUT/yacm-$V-windows-arm64
L=$OUT/yacm-$V-linux-amd64
DW=$OUT/yacm-$V-demo-windows
DL=$OUT/yacm-$V-demo-linux-amd64
echo "Packaging YACM $V"
rm -rf "$OUT"
mkdir -p "$WX" "$WA" "$L" "$DW" "$DL"

if [ "${SKIP_FRONTEND:-}" != 1 ]; then
	(cd frontend && npm ci --no-audit --no-fund && npm run build)
fi

# Windows (the rsrc_windows_*.syso files add icon, manifest and version info)
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -tags desktop,production -ldflags "-s -w -H windowsgui" -o "$WX/YACM.exe" .
GOOS=windows GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -tags desktop,production -ldflags "-s -w -H windowsgui" -o "$WA/YACM.exe" .

# Linux
go build -trimpath -tags desktop,production,webkit2_41 -ldflags "-s -w" -o "$L/yacm" .

# Demo: the x64 device also runs on Windows on ARM.
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$DW/yacm-demo-device.exe" ./cmd/yacm-demo-device
CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o "$DL/yacm-demo-device" ./cmd/yacm-demo-device
go run ./tools/mkdemo "$DW/demo.yacm" >/dev/null
cp "$DW/demo.yacm" "$DL/"
cp build/demo/README-DEMO.txt "$DW/"
sed 's/yacm-demo-device\.exe/yacm-demo-device/g; s/Start it by double-clicking; close its window (or press Ctrl+C) to stop\./Start it in a terminal (.\/yacm-demo-device); press Ctrl+C to stop./' \
	build/demo/README-DEMO.txt >"$DL/README-DEMO.txt"

for d in "$WX" "$WA" "$L"; do cp README.md LICENSE NOTICE.md "$d/"; done
for d in "$DW" "$DL"; do cp LICENSE NOTICE.md "$d/"; done

(cd "$WX" && sha256sum YACM.exe >SHA256SUMS.txt)
(cd "$WA" && sha256sum YACM.exe >SHA256SUMS.txt)
(cd "$L" && sha256sum yacm >SHA256SUMS.txt)
(cd "$DW" && sha256sum yacm-demo-device.exe demo.yacm >SHA256SUMS.txt)
(cd "$DL" && sha256sum yacm-demo-device demo.yacm >SHA256SUMS.txt)
(
	cd "$OUT"
	for d in "yacm-$V-windows-x64" "yacm-$V-windows-arm64" "yacm-$V-demo-windows"; do zip -qr "$d.zip" "$d"; done
	for d in "yacm-$V-linux-amd64" "yacm-$V-demo-linux-amd64"; do tar czf "$d.tar.gz" "$d"; done
)
ls -la "$OUT"
