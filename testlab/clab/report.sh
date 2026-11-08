#!/bin/bash
# Prints the lab's failures as GitHub annotations (::error), so they show on
# the run's summary page: readiness timeouts, deploy errors and failed tests.
cd "$(dirname "$0")/.work" 2>/dev/null || exit 0
esc() { sed -e 's/%/%25/g' -e ':a;N;$!ba;s/\n/%0A/g'; }
if [ -f up.log ]; then
	grep -E "ERRO |Error|timeout:" up.log | head -8 | while read -r l; do echo "::error title=Lab start::$l"; done
	tail -25 up.log | esc | sed 's/^/::notice title=Lab start (last lines)::/'
	echo
fi
if [ -f e2e.log ]; then
	# go test -v prints a test's log lines before its --- FAIL line.
	awk '/^=== RUN/{buf=""; next} /^--- FAIL/{print $3 "\t" $0 buf; buf=""; next} /^--- /{buf=""; next} /^    /{buf=buf "\\n" $0}' e2e.log |
		while IFS=$'\t' read -r name body; do
			printf '::error title=%s::%s\n' "$name" "$(printf '%b' "$body" | head -40 | esc)"
		done
	grep -E "^(ok|FAIL|panic)" e2e.log | tail -1 | while read -r l; do echo "::notice title=Result::$l"; done
	# GitHub shows at most 10 notices per step: the result and a few log lines.
	grep -E "^    lab_e2e_test.go:[0-9]+: " e2e.log | grep -v -E "needs the Arista" | head -8 | while read -r l; do echo "::notice title=Log::$l"; done
fi
