# shellcheck shell=bash
# Sourced by scripts/package-install-test.sh: turns the install snippets of docs/install.md into
# the commands a container runs, so the documented file names are the real ones (ADR 0025 slice 8).
#
# install_commands <doc> <snippet id> <staging dir> <version> <arch>
#
# The snippet is the fenced block that follows <!-- install-snippet: <id> -->. Every line is one
# "apt install ./file" or "dnf install ./file" command; <version> and <arch> are substituted, a
# leading sudo is dropped (the container runs as root), trailing comments are removed, and a line
# whose file is not in the staging directory is skipped only for the optional console package; for
# any other package it is an error. apt and dnf
# get their non-interactive flags. Any other command in a snippet is an error.
install_commands() {
  local doc=$1 id=$2 staging=$3 version=$4 arch=$5 block line word present
  block=$(awk -v id="$id" '
    $0 == "<!-- install-snippet: " id " -->" { want = 1; next }
    want && /^```/ { if (fence) exit; fence = 1; next }
    want && fence { print }
  ' "$doc") || return 1
  [[ -n "$block" ]] || { printf 'install_commands: no install-snippet %s in %s\n' "$id" "$doc" >&2; return 1; }
  while IFS= read -r line; do
    line=${line%%#*}
    line=${line%"${line##*[![:space:]]}"}
    [[ -n "${line//[[:space:]]/}" ]] || continue
    line=${line//<version>/$version}
    line=${line//<arch>/$arch}
    line=${line#sudo }
    present=1
    for word in $line; do
      case $word in
        ./*) [[ -f "$staging/${word#./}" ]] || present=0 ;;
      esac
    done
    if [[ "$present" != 1 ]]; then
      # Only the console package is built on one leg alone (amd64) and may be absent; a missing
      # server or client package is a failure, not a skipped line.
      case $line in
        *notarx-console*) continue ;;
      esac
      printf 'install_commands: the file of this snippet line is not in %s: %s\n' "$staging" "$line" >&2
      return 1
    fi
    case $line in
      'apt install '*) printf 'apt-get install -y -qq %s\n' "${line#apt install }" ;;
      'dnf install '*) printf 'dnf install -y -q %s\n' "${line#dnf install }" ;;
      *) printf 'install_commands: unsupported snippet line: %s\n' "$line" >&2; return 1 ;;
    esac
  done <<<"$block"
}
