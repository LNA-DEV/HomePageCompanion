#!/usr/bin/env bash
# Runs a go command in the core module and in every module under modules/,
# e.g. scripts/go-each.sh test -race ./...
set -euo pipefail
cd "$(dirname "$0")/.."
status=0
for dir in . modules/*/; do
	dir="${dir%/}"
	[[ -f "$dir/go.mod" ]] || continue
	echo "== $dir: go $*"
	(cd "$dir" && go "$@") || status=1
done
exit $status
