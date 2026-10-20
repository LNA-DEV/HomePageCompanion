#!/usr/bin/env bash
# Runs check-tests.sh in the core module and in every module under modules/,
# each with its own .notests exception list.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
status=0
for dir in "$root" "$root"/modules/*/; do
	dir="${dir%/}"
	[[ -f "$dir/go.mod" ]] || continue
	echo "== ${dir#"$root"/}"
	(cd "$dir" && "$root/scripts/check-tests.sh" .notests) || status=1
done
exit $status
