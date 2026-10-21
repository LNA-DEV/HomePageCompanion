#!/usr/bin/env bash
# Records the old companion's answers to the website's requests into
# e2e/testdata/golden (the gate "the website does not notice",
# docs/concepts/modular-architecture.md §6). Run it while src/ still holds
# the old companion; the fixture is e2e/testdata/legacy (written by
# src/cmd/legacyfixture). It needs port 8080.
set -euo pipefail
root="$(cd "$(dirname "$0")/.." && pwd)"
bin="$(mktemp -d)/old-companion"
(cd "$root/src" && GOWORK=off go build -o "$bin" .)
rm -rf "$root/e2e/testdata/golden"
(cd "$root/e2e" && go test -count=1 -run TestWebsiteUnchanged -record "$bin" .)
echo "recorded $(ls "$root/e2e/testdata/golden" | wc -l) answers into e2e/testdata/golden"
