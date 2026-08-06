#!/usr/bin/env bash
# assert-second-boot.sh — post-boot assertions.
#
# Uploaded and executed by the "shell" provisioner in
# template-second-boot.pkr.hcl AFTER the artifact of build 1 has been booted
# a second time (fresh boot). The boot-time systemd-sysext.service /
# systemd-confext.service re-apply the persisted extensions, so this script
# verifies that persist mode survives a fresh boot: status merged, extension
# files visible, services enabled, images still present in the install dirs.
#
# Same exact matching discipline as assert.sh: only the positive merged
# markers satisfy the check; "STATUS: NOT MERGED" can never pass.
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

merged_state() {
  local cmd="$1" out
  if ! out="$("$cmd" status 2>&1)"; then
    fail "$cmd status exited non-zero: $out"
  fi
  if merged_report "$out"; then
    pass "$cmd status reports merged after fresh boot"
  else
    fail "$cmd status does not report merged after fresh boot: $(printf '%s' "$out" | tr '\n' ' ')"
  fi
}

unit_enabled() {
  local unit="$1" state
  state="$(systemctl is-enabled "$unit" 2>&1 || true)"
  if [ "$state" = "enabled" ]; then
    pass "$unit is enabled after fresh boot"
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

# --- wait for the boot-time merge ----------------------------------------
# packer connects to SSH as soon as the guest is up; the boot-time merge runs
# shortly after, so wait for it (up to 5 minutes) before asserting. Only the
# positive merged markers satisfy the wait.

merge_ready=0
for _ in $(seq 1 60); do
  out="$(systemd-sysext status 2>&1 || true)"
  if merged_report "$out"; then
    merge_ready=1
    break
  fi
  sleep 5
done
if [ "${merge_ready}" != "1" ]; then
  fail "systemd-sysext did not report merged within 5m of the fresh boot"
fi
pass "systemd-sysext reports merged after fresh boot"

# --- merged after a fresh boot --------------------------------------------

merged_state systemd-sysext
merged_state systemd-confext

# --- boot services still enabled ------------------------------------------

unit_enabled systemd-sysext.service
unit_enabled systemd-confext.service

# --- extension files visible at merged paths after boot -------------------

must_be_exec /usr/bin/sysext-persist-hello "sysext file visible at merged /usr/bin path after fresh boot"
must_exist /etc/confext-persist-hello.conf "confext file visible at merged /etc path after fresh boot"

# --- images still in the install directories ------------------------------

must_exist /var/lib/extensions/sysext-persist-hello "sysext image remains in /var/lib/extensions"
must_exist /var/lib/confexts/confext-persist-hello "confext image remains in /var/lib/confexts"

# --- functional check of the merged executable ---------------------------

if [ "$(/usr/bin/sysext-persist-hello)" = "hello from sysext persist" ]; then
  pass "/usr/bin/sysext-persist-hello runs and prints expected output after fresh boot"
else
  fail "/usr/bin/sysext-persist-hello did not run as expected after fresh boot"
fi

echo "assert-second-boot.sh: all post-boot assertions passed"
