#!/usr/bin/env bash
# Installs the WebKitGTK build dependencies on a GitHub Ubuntu runner.
# The runner's package mirror is sometimes extremely slow, so every attempt
# is capped at 8 minutes; downloaded packages stay in apt's cache, so a
# retry goes on where the last one stopped, and from the second attempt on
# apt uses archive.ubuntu.com instead of the runner's mirror.
set -uo pipefail

sudo rm -f /var/lib/man-db/auto-update
opts=(-o Acquire::Retries=3 -o Acquire::http::Timeout=30)
for attempt in 1 2 3 4; do
	echo "apt attempt $attempt"
	if [ "$attempt" -gt 1 ]; then
		for f in /etc/apt/sources.list /etc/apt/sources.list.d/*.list /etc/apt/sources.list.d/*.sources; do
			[ -f "$f" ] || continue
			sudo sed -i -E 's#mirror\+file:/etc/apt/apt-mirrors.txt#http://archive.ubuntu.com/ubuntu/#; s#http://azure\.archive\.ubuntu\.com/ubuntu/?#http://archive.ubuntu.com/ubuntu/#' "$f"
		done
	fi
	if sudo timeout 480 apt-get update "${opts[@]}" &&
		sudo timeout 480 apt-get install -y --no-install-recommends "${opts[@]}" \
			libgtk-3-dev libwebkit2gtk-4.1-dev; then
		exit 0
	fi
	sleep 10
done
echo "::error::Could not install libgtk-3-dev and libwebkit2gtk-4.1-dev"
exit 1
