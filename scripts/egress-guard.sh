#!/usr/bin/env bash
# Run a command that must not reach the internet: egress-guard.sh COMMAND [ARGS...]
#
# Hermetic Maven and Gradle lanes (issue #358, docs/ci.md): a Maven or Gradle
# client, or a Go test that drives one, may talk to loopback and private ranges
# only (the fixture mirror, the service containers and the system under test).
# A request to any other address is rejected by the kernel, so a test that
# reaches Maven Central or a plugin portal fails at once.
#
# NOTARX_EGRESS_GUARD=1 (set by .github/workflows/contract-lane.yml) enforces the
# guard on a Linux runner; without it the command runs unchanged, so a developer
# machine is not touched. Enforcement needs passwordless sudo, iptables and
# setpriv. The command runs with the group notarx-egress as its effective group
# and an iptables OWNER match on that group rejects every packet to a public
# address. Before the command starts, the guard proves it is in force by
# failing to connect to a public address; if it cannot, the command does not run.
set -euo pipefail

if [[ $# -eq 0 ]]; then
  printf 'usage: egress-guard.sh COMMAND [ARGS...]\n' >&2
  exit 2
fi
if [[ "${NOTARX_EGRESS_GUARD:-}" != "1" ]]; then
  exec "$@"
fi
for tool in sudo iptables setpriv timeout; do
  command -v "$tool" >/dev/null || { printf 'egress guard requires %s\n' "$tool" >&2; exit 1; }
done

group=notarx-egress
chain=NOTARX_EGRESS
getent group "$group" >/dev/null || sudo -n groupadd --system "$group"
gid=$(getent group "$group" | cut -d: -f3)

install_rules() {
  local tool=$1; shift
  local local_ranges=("$@")
  # Rebuild the chain on every call: a chain left half-built by an earlier run
  # is replaced, and the hook into OUTPUT is added once.
  sudo -n "$tool" -w -N "$chain" 2>/dev/null || true
  sudo -n "$tool" -w -F "$chain"
  local range
  for range in "${local_ranges[@]}"; do
    sudo -n "$tool" -w -A "$chain" -d "$range" -j ACCEPT
  done
  sudo -n "$tool" -w -A "$chain" -j REJECT
  sudo -n "$tool" -w -C OUTPUT -m owner --gid-owner "$gid" -j "$chain" 2>/dev/null ||
    sudo -n "$tool" -w -I OUTPUT 1 -m owner --gid-owner "$gid" -j "$chain"
}
install_rules iptables 127.0.0.0/8 10.0.0.0/8 172.16.0.0/12 192.168.0.0/16
if command -v ip6tables >/dev/null; then
  install_rules ip6tables ::1/128 fc00::/7 fe80::/10
fi

run_guarded() {
  sudo -n --preserve-env setpriv --reuid="$(id -u)" --regid="$gid" --groups="$(id -G | tr ' ' ',')" \
    env HOME="$HOME" PATH="$PATH" "$@"
}

# Prove enforcement. A positive control first shows the wrapper itself works
# (so the probe cannot fail for an unrelated reason such as a broken sudo), then
# the public-address probe must be refused. The IPv6 hook is checked by rule.
run_guarded true || { printf 'egress guard cannot run commands as group %s\n' "$group" >&2; exit 1; }
sudo -n iptables -w -C OUTPUT -m owner --gid-owner "$gid" -j "$chain"
if command -v ip6tables >/dev/null; then
  sudo -n ip6tables -w -C OUTPUT -m owner --gid-owner "$gid" -j "$chain"
fi
if run_guarded timeout 5 bash -c 'exec 3<>/dev/tcp/1.1.1.1/443' 2>/dev/null; then
  printf 'egress guard is not in force: a public address was reachable\n' >&2
  exit 1
fi

run_guarded "$@"
