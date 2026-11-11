#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.
#
# Builds the MSIX package of YACM for the Microsoft Store:
#
#   build/windows/msix.sh YACM.exe ARCH VERSION OUT.msix [NOTICES]
#   build/windows/msix.sh bundle VERSION OUT.msixbundle X64.msix ARM64.msix
#
# ARCH is x64 or arm64, VERSION the YACM version (0.9.3, or four numbers;
# the Store wants the last one 0). NOTICES is THIRD-PARTY-NOTICES.txt.
# The bundle (Windows only) joins the packages of both architectures into
# the one file that is uploaded to Partner Center.
#
# On Windows (Git Bash; the release workflow) it uses makeappx.exe and
# makepri.exe from the newest Windows SDK, or the ones MAKEAPPX and MAKEPRI
# name. makepri writes resources.pri, the index through which Windows finds
# the logo for each display scale and icon size (msix-assets.sh draws them).
#
# Elsewhere it uses makemsix from github.com/microsoft/msix-packaging (built
# with --pack; MAKEMSIX names it). There is no makepri there, so that
# package carries the 100 % logos only: fine for a test, not for the Store.
#
# The package is unsigned: the Store signs it. For a test install, sign it
# with a certificate whose subject is the Publisher in AppxManifest.xml.
set -euo pipefail

# Git Bash would rewrite switches like /o into paths.
export MSYS_NO_PATHCONV=1 MSYS2_ARG_CONV_EXCL='*'

# pkgversion 0.9.3-rc.1 prints 0.9.3.0 (a suffix is not a package version).
pkgversion() {
	local v=${1%%-*}
	case $v in
	*.*.*.*) echo "$v" ;;
	*.*.*) echo "$v.0" ;;
	*) echo "version $1: want three or four numbers" >&2; return 1 ;;
	esac
}

# sdk sets MAKEAPPX and MAKEPRI to the tools of the newest Windows SDK.
sdk() {
	if [ -z "${MAKEAPPX:-}" ]; then
		MAKEAPPX=$(ls -d "/c/Program Files (x86)/Windows Kits/10/bin/"10.*/x64/makeappx.exe 2>/dev/null | sort -V | tail -1)
		[ -n "$MAKEAPPX" ] || { echo "makeappx.exe not found: install the Windows SDK or set MAKEAPPX" >&2; exit 1; }
	fi
	MAKEPRI=${MAKEPRI:-$(dirname "$MAKEAPPX")/makepri.exe}
}

if [ "$1" = bundle ]; then
	VERSION=$(pkgversion "$2") OUT=$3
	shift 3
	sdk
	DIR=$(mktemp -d)
	trap 'rm -rf "$DIR"' EXIT
	cp "$@" "$DIR/"
	rm -f "$OUT"
	"$MAKEAPPX" bundle /o /bv "$VERSION" /d "$(cygpath -w "$DIR")" /p "$(cygpath -w "$OUT")" >"$DIR/makeappx.log" || { cat "$DIR/makeappx.log" >&2; exit 1; }
	[ -s "$OUT" ]
	echo "$OUT: YACM $VERSION, $# packages"
	exit 0
fi

EXE=$1 ARCH=$2 OUT=$4 NOTICES=${5:-}
VERSION=$(pkgversion "$3")
HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)

DIR=$(mktemp -d)
WORK=$(mktemp -d)
trap 'rm -rf "$DIR" "$WORK"' EXIT
cp "$EXE" "$DIR/YACM.exe"
cp -r "$HERE/msix/Assets" "$DIR/"
cp "$ROOT/LICENSE" "$ROOT/NOTICE.md" "$ROOT/README.md" "$DIR/"
[ -n "$NOTICES" ] && cp "$NOTICES" "$DIR/THIRD-PARTY-NOTICES.txt"
sed -e "s|\${VERSION}|$VERSION|; s|\${ARCH}|$ARCH|" "$HERE/msix/AppxManifest.xml" >"$DIR/AppxManifest.xml"

rm -f "$OUT"
case $(uname -s) in
MINGW* | MSYS* | CYGWIN*)
	sdk
	W=$(cygpath -w "$DIR")
	CF=$(cygpath -w "$WORK/priconfig.xml")
	"$MAKEPRI" createconfig /cf "$CF" /dq en-US /pv 10.0.0 /o >/dev/null
	# One package holds every scale: without this part makepri would
	# write a separate resource file for each.
	sed -i '/<packaging>/,/<\/packaging>/d' "$WORK/priconfig.xml"
	"$MAKEPRI" new /pr "$W" /cf "$CF" /of "$W\\resources.pri" /o >"$WORK/makepri.log" || { cat "$WORK/makepri.log" >&2; exit 1; }
	[ "$(ls "$DIR"/*.pri | wc -l)" = 1 ] || { echo "makepri wrote more than resources.pri:" >&2; ls "$DIR" >&2; exit 1; }
	"$MAKEAPPX" pack /o /d "$W" /p "$(cygpath -w "$OUT")" >"$WORK/makeappx.log" || { cat "$WORK/makeappx.log" >&2; exit 1; }
	;;
*)
	# No resources.pri: Windows then looks for the plain file names.
	for f in "$DIR"/Assets/*.scale-100.png; do cp "$f" "${f%.scale-100.png}.png"; done
	"${MAKEMSIX:-makemsix}" pack -d "$DIR" -p "$OUT" | grep -v "^Microsoft (R)\|^Copyright" || true
	;;
esac
[ -s "$OUT" ]
echo "$OUT: YACM $VERSION $ARCH"
