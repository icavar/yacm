#!/usr/bin/env bash
# Runs every fuzz test for a while (default 15 s each): bash build/fuzz.sh 5m
# The parsers read files from other people (other programs' configurations,
# .yacm files, team libraries, .ppk keys), so they must survive anything.
set -euo pipefail
cd "$(dirname "$0")/.."
T=${1:-15s}
targets=(
	"internal/importer FuzzForeign"
	"internal/importer FuzzCSV"
	"internal/importer FuzzXLSX"
	"internal/exchange FuzzParse"
	"internal/team FuzzParse"
	"internal/ppk FuzzParse"
	"internal/logmask FuzzMasker"
)
for t in "${targets[@]}"; do
	set -- $t
	echo "== $1 $2 ($T)"
	go test -run '^$' -fuzz "^$2\$" -fuzztime "$T" -fuzzminimizetime 10s "./$1"
done
