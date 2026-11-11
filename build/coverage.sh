#!/usr/bin/env bash
# Summarise a Go coverage profile per package and check the floors of the
# packages that guard secrets and other people's data. Usage:
#   go test -coverpkg=./... -coverprofile=coverage.out ./...
#   bash build/coverage.sh coverage.out
set -euo pipefail
profile=${1:-coverage.out}

# Minimum statement coverage (%) per critical package. Raise them as tests
# are added; a drop below fails CI.
declare -A floor=(
  [github.com/yacm-app/yacm/internal/vault]=75
  [github.com/yacm-app/yacm/internal/store]=70
  [github.com/yacm-app/yacm/internal/team]=75
  [github.com/yacm-app/yacm/internal/exchange]=80
  [github.com/yacm-app/yacm/internal/sshconn]=70
  [github.com/yacm-app/yacm/internal/importer]=75
  [github.com/yacm-app/yacm/internal/logmask]=85
  [github.com/yacm-app/yacm/internal/localcmd]=80
)

# With -coverpkg every test binary reports every block: a block counts as
# covered when any of them ran it.
summary=$(awk -F'[: ]' 'NR > 1 {
  key = $1 ":" $2; stmts[key] = $3; if ($4 > 0) hit[key] = 1
} END {
  for (k in stmts) {
    split(k, p, ":"); n = split(p[1], parts, "/"); pkg = parts[1]
    for (i = 2; i < n; i++) pkg = pkg "/" parts[i]
    total[pkg] += stmts[k]; if (k in hit) cov[pkg] += stmts[k]
  }
  for (q in total) printf "%s %.1f\n", q, (total[q] ? 100 * cov[q] / total[q] : 0)
}' "$profile" | sort)

failed=0
{
  echo "### Test coverage (statements)"
  echo
  echo "| Package | Coverage | Floor |"
  echo "|---|---:|---:|"
  while read -r pkg pct; do
    f=${floor[$pkg]:-}
    mark=""
    if [ -n "$f" ] && awk -v a="$pct" -v b="$f" 'BEGIN { exit !(a < b) }'; then mark=" ❌"; failed=1; fi
    echo "| ${pkg#github.com/yacm-app/yacm/} | ${pct}%${mark} | ${f:+$f%} |"
  done <<< "$summary"
} | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"

total=$(go tool cover -func="$profile" | awk '/^total:/ { print $3 }')
echo
echo "Total: $total"
if [ "$failed" = 1 ]; then
  echo "::error title=Coverage::A critical package dropped below its coverage floor (see the table in the run summary)"
  exit 1
fi
