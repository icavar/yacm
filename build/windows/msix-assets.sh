#!/bin/bash
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 The YACM Authors. Additional terms under GPLv3 section 7: see NOTICE.md.
#
# Draws the MSIX logos in build/windows/msix/Assets from build/icon.svg, in
# every scale Windows asks for (100 to 400 %) and, for the taskbar and Start
# list, in exact pixel sizes without a plate. Run it after the icon changes
# and commit the result. Needs rsvg-convert and ImageMagick (convert).
set -euo pipefail

cd "$(dirname "$0")"
SVG=../icon.svg
OUT=msix/Assets
rm -rf "$OUT"
mkdir -p "$OUT"

# draw NAME WIDTH HEIGHT ICON: the icon, ICON pixels wide, in the middle of
# a transparent WIDTH x HEIGHT image.
draw() {
	rsvg-convert -w "$4" -h "$4" "$SVG" |
		convert png:- -background none -gravity center -extent "$2x$3" -strip "PNG32:$OUT/$1.png"
}

for s in 100 125 150 200 400; do
	px() { echo $((($1 * s + 50) / 100)); }
	draw "Square44x44Logo.scale-$s" "$(px 44)" "$(px 44)" "$(px 42)"
	draw "Square150x150Logo.scale-$s" "$(px 150)" "$(px 150)" "$(px 96)"
	draw "Wide310x150Logo.scale-$s" "$(px 310)" "$(px 150)" "$(px 96)"
	draw "StoreLogo.scale-$s" "$(px 50)" "$(px 50)" "$(px 48)"
done
for t in 16 24 32 48 256; do
	draw "Square44x44Logo.targetsize-$t" "$t" "$t" "$t"
	cp "$OUT/Square44x44Logo.targetsize-$t.png" "$OUT/Square44x44Logo.targetsize-${t}_altform-unplated.png"
	cp "$OUT/Square44x44Logo.targetsize-$t.png" "$OUT/Square44x44Logo.targetsize-${t}_altform-lightunplated.png"
done
ls "$OUT" | wc -l
