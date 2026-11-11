#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.
#
# Builds the MSIX package of YACM for the Microsoft Store:
#
#   build/windows/msix.sh YACM.exe ARCH VERSION OUT.msix [NOTICES]
#
# ARCH is x64 or arm64, VERSION the YACM version (0.9.3, or four numbers;
# the Store wants the last one 0). NOTICES is THIRD-PARTY-NOTICES.txt.
# MAKEMSIX names makemsix from github.com/microsoft/msix-packaging (built
# with --pack); on Windows, MAKEAPPX may name makeappx.exe instead. The
# package is unsigned: the Store signs it. For a test install, sign it with
# a certificate whose subject is the Publisher in AppxManifest.xml.
set -euo pipefail

EXE=$1 ARCH=$2 VERSION=$3 OUT=$4 NOTICES=${5:-}
case $VERSION in
*.*.*.*) ;;
*.*.*) VERSION=$VERSION.0 ;;
*) echo "version $VERSION: want three or four numbers" >&2; exit 1 ;;
esac
VERSION=${VERSION%%-*} # 0.9.3-rc.1 is not a package version
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)

DIR=$(mktemp -d)
trap 'rm -rf "$DIR"' EXIT
cp "$EXE" "$DIR/YACM.exe"
cp -r "$HERE/msix/Assets" "$DIR/"
cp "$ROOT/LICENSE" "$ROOT/NOTICE.md" "$ROOT/README.md" "$DIR/"
[ -n "$NOTICES" ] && cp "$NOTICES" "$DIR/THIRD-PARTY-NOTICES.txt"
sed -e "s|\${VERSION}|$VERSION|; s|\${ARCH}|$ARCH|" "$HERE/msix/AppxManifest.xml" >"$DIR/AppxManifest.xml"

rm -f "$OUT"
if [ -n "${MAKEAPPX:-}" ]; then
	"$MAKEAPPX" pack /o /d "$(cygpath -w "$DIR" 2>/dev/null || echo "$DIR")" /p "$OUT" >/dev/null
else
	"${MAKEMSIX:-makemsix}" pack -d "$DIR" -p "$OUT" | grep -v "^Microsoft (R)\|^Copyright" || true
	[ -s "$OUT" ]
fi
echo "$OUT: YACM $VERSION $ARCH"
