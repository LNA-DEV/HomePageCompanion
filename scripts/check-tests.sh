#!/usr/bin/env bash
# Fails when a Go package has no test file and is not on the exception list
# (docs/concepts/modular-architecture.md §6, CI gate 4). Run it from a module
# root; the exception file lists package paths relative to that module, one
# per line, "#" starts a comment.
set -euo pipefail

exceptions_file="${1:-}"
declare -A allowed=()
if [[ -n "$exceptions_file" && -f "$exceptions_file" ]]; then
	while read -r line || [[ -n "$line" ]]; do
		line="${line%%#*}"
		line="${line//[[:space:]]/}"
		[[ -n "$line" ]] && allowed["$line"]=1
	done <"$exceptions_file"
fi

module="$(awk '/^module /{print $2; exit}' go.mod)"
missing=()
while read -r pkg tests xtests; do
	rel="${pkg#"$module"}"
	rel="${rel#/}"
	[[ -z "$rel" ]] && rel="."
	if [[ "$tests" == "0" && "$xtests" == "0" && -z "${allowed[$rel]:-}" ]]; then
		missing+=("$rel")
	fi
done < <(go list -f '{{.ImportPath}} {{len .TestGoFiles}} {{len .XTestGoFiles}}' ./...)

if ((${#missing[@]})); then
	echo "packages without tests (add tests, or list them in ${exceptions_file:-an exception file}):"
	printf '  %s\n' "${missing[@]}"
	exit 1
fi
echo "every package has tests or is on the exception list"
