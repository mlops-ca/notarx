#!/usr/bin/env bash
# Checks the optional OpenTofu module deploy/oci-free-tier/tofu (ADR 0017 slice 7):
# format, validate (needs the oracle/oci provider from the registry) and tflint,
# and that no state, variable values or backend configuration are committed.
set -euo pipefail

repo_root=$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)
module="$repo_root/deploy/oci-free-tier/tofu"
for binary in tofu tflint; do command -v "$binary" >/dev/null || { printf 'The OpenTofu module check requires %s\n' "$binary" >&2; exit 1; }; done

if tracked=$(git -C "$repo_root" ls-files -- deploy/oci-free-tier/tofu | grep -E '(\.tfstate|\.tfstate\..*|\.tfvars|(^|/)backend\.tf)$'); then
  printf 'State, variable values or backend configuration must not be committed:\n%s\n' "$tracked" >&2
  exit 1
fi

data_dir=$(mktemp -d)
trap 'rm -rf "$data_dir"' EXIT
export TF_DATA_DIR="$data_dir"
cd "$module"
tofu fmt -check -diff -recursive
tofu init -backend=false -input=false >/dev/null
tofu validate
tflint --init >/dev/null
tflint --config "$module/.tflint.hcl"
echo 'oci-free-tier tofu: fmt, validate and tflint passed'
