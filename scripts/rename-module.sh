#!/usr/bin/env bash
# Rewrite the Go module path from the upstream identity to the notarx fork
# (ADR 0013, "Rename mechanics and order", step 2). Idempotent: a second run
# finds nothing to change. Historical and attribution files keep the old path.
set -euo pipefail

root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
cd "$root"

old='github.com/ccsert/artifact-gateway'
new='github.com/mlops-ca/notarx'

# Files that must keep the upstream identity (attribution, history, ADRs).
keep_re='^(LICENSE|NOTICE|docs/upstream\.md|CHANGELOG[^/]*|docs/adr/.*)$'

changed=()
while IFS= read -r -d '' path; do
  if [[ "$path" =~ $keep_re ]]; then
    continue
  fi
  [[ -f "$path" && ! -L "$path" ]] || continue
  grep -Iq . "$path" 2>/dev/null || continue
  grep -Fq "$old" "$path" || continue
  changed+=("$path")
done < <(git ls-files -z)

if ((${#changed[@]} == 0)); then
  printf 'rename-module: nothing to rewrite.\n'
  exit 0
fi

# Literal replacement; perl avoids sed -i differences between GNU and BSD.
OLD="$old" NEW="$new" perl -pi -e 's/\Q$ENV{OLD}\E/$ENV{NEW}/g' "${changed[@]}"

go_files=()
for path in "${changed[@]}"; do
  [[ "$path" == *.go ]] && go_files+=("$path")
done
if ((${#go_files[@]} > 0)); then
  gofmt -w "${go_files[@]}"
fi

printf 'rename-module: rewrote %d files.\n' "${#changed[@]}"
