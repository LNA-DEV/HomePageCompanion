#!/usr/bin/env bash
# Runs a go command in the core module, in every module under modules/ and
# in e2e/, e.g. scripts/go-each.sh test -race ./...
set -euo pipefail
cd "$(dirname "$0")/.."
status=0
for dir in . modules/*/ e2e/; do
	dir="${dir%/}"
	[[ -f "$dir/go.mod" ]] || continue
	echo "== $dir: go $*"
	(cd "$dir" && go "$@") || status=1
done
exit $status
