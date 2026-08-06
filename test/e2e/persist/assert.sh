#!/usr/bin/env bash
# assert.sh — during-build assertions.
#
# Uploaded and executed by the packer "shell" provisioner AFTER the sysext
# and confext persist provisioners have run (merge_during_build=true), so it
# runs while /usr and /etc are overmounted by the merged extensions.
#
# The assertions are deliberately exact: "STATUS: NOT MERGED" contains the
# substring "MERGED", so a naive grep for "merged" would falsely pass. We
# match the positive merged markers from the upstream output formats
# (systemd < 255: "SYSEXTS ARE MERGED"; systemd >= 255: "STATUS: MERGED")
# and the table format that systemd 255.4 on Ubuntu 24.04 actually prints
# (a hierarchy row listing an extension instead of "none").
set -euo pipefail

# --- helpers -------------------------------------------------------------

fail() {
  echo "ASSERT FAIL: $*" >&2
  exit 1
}

pass() {
  echo "ASSERT PASS: $*"
}

# table_merged detects the merged state in the tabular `status` output of
# systemd 255.4 (Ubuntu 24.04): a hierarchy row (line starting with a path)
# whose EXTENSIONS column is not "none" means the hierarchy is merged.
table_merged() {
  local out="$1"
  printf '%s\n' "$out" | awk '/^\// { if ($2 != "" && $2 != "none") found=1 } END { exit !found }'
}

# merged_report returns 0 when `status` output reports the merged state in
# either the marker or the table format.
merged_report() {
  case "$1" in
    *"STATUS: MERGED"* | *"SYSEXTS ARE MERGED"*) return 0 ;;
  esac
  table_merged "$1"
}

# merged_state asserts that `<cmd> status` reports the merged state.
merged_state() {
  local cmd="$1" out
  if ! out="$("$cmd" status 2>&1)"; then
    fail "$cmd status exited non-zero: $out"
  fi
  if merged_report "$out"; then
    pass "$cmd status reports merged"
  else
    fail "$cmd status does not report merged: $(printf '%s' "$out" | tr '\n' ' ')"
  fi
}

# unit_enabled asserts that `systemctl is-enabled <unit>` prints "enabled".
unit_enabled() {
  local unit="$1" state
  state="$(systemctl is-enabled "$unit" 2>&1 || true)"
  if [ "$state" = "enabled" ]; then
    pass "$unit is enabled"
  else
    fail "$unit is-enabled is '$state', expected enabled"
  fi
}

must_exist() { # must_exist <path> <description>
  if [ -e "$1" ]; then
    pass "$2"
  else
    fail "$2 (missing: $1)"
  fi
}

must_be_exec() { # must_be_exec <path> <description>
  if [ -x "$1" ]; then
    pass "$2"
  else
    fail "$2 (not executable or missing: $1)"
  fi
}

# --- merged during the build ----------------------------------------------

merged_state systemd-sysext
merged_state systemd-confext

# --- boot services enabled ------------------------------------------------

unit_enabled systemd-sysext.service
unit_enabled systemd-confext.service

# --- extension files visible at their merged paths ------------------------

must_be_exec /usr/bin/sysext-persist-hello "sysext file visible at merged /usr/bin path"
must_exist /etc/confext-persist-hello.conf "confext file visible at merged /etc path"

# --- images REMAIN in the install directories -----------------------------

must_exist /var/lib/extensions/sysext-persist-hello "sysext image remains in /var/lib/extensions"
must_exist /var/lib/extensions/sysext-persist-hello/usr/bin/sysext-persist-hello "sysext packaged tree intact"
must_exist /var/lib/extensions/sysext-persist-hello/usr/lib/extension-release.d/extension-release.sysext-persist-hello "sysext release file shipped inside image"
must_exist /var/lib/confexts/confext-persist-hello "confext image remains in /var/lib/confexts"
must_exist /var/lib/confexts/confext-persist-hello/etc/confext-persist-hello.conf "confext packaged tree intact"
must_exist /var/lib/confexts/confext-persist-hello/etc/extension-release.d/extension-release.confext-persist-hello "confext release file shipped inside image"

# --- functional check of the merged executable ---------------------------

if [ "$(/usr/bin/sysext-persist-hello)" = "hello from sysext persist" ]; then
  pass "/usr/bin/sysext-persist-hello runs and prints expected output"
else
  fail "/usr/bin/sysext-persist-hello did not run as expected"
fi

echo "systemd: $(systemctl --version | head -1)"
echo "assert.sh: all during-build assertions passed"
