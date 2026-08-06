#!/usr/bin/env bash
# E2E bake-mode guest assertions.
#
# Runs inside the cloud-hypervisor guest via the packer "shell" provisioner
# after the sysext/confext provisioners completed in bake mode. Every check
# prints a PASS/FAIL line; the script exits non-zero if any check failed so
# the packer build (and therefore run.sh) fails loudly.
#
# Conditions covered:
#   1. sysext file present at its merged /usr/... path
#   2. sysext .raw file present at its merged /usr/... path
#   3. confext file present at its merged /etc/... path
#   4. /var/lib/extensions and /var/lib/confexts empty (bake removes them)
#   5. baked extension-release.d files absent
# Edge coverage: content integrity, executable mode, empty-vs-missing dirs,
# and a non-fatal probe of the boot-service enablement state.
set -u

FAILURES=0

check() { # check <label> <command...>
    local label="$1"
    shift
    if "$@"; then
        printf 'PASS: %s\n' "$label"
    else
        printf 'FAIL: %s\n' "$label"
        FAILURES=$((FAILURES + 1))
    fi
}

dir_empty() { # dir_empty <dir> - true when <dir> contains no entries
    [ -z "$(ls -A "$1" 2>/dev/null)" ]
}

check "sysext directory source present at merged path /usr/bin/sysext-hello" \
    test -f /usr/bin/sysext-hello

check "sysext directory source executable (mode preserved through bake)" \
    test -x /usr/bin/sysext-hello

check "sysext directory source content intact" \
    grep -q 'E2E_SYSEXT_HELLO_OK' /usr/bin/sysext-hello

check "sysext .raw source present at merged path /usr/bin/sysext-raw-hello" \
    test -f /usr/bin/sysext-raw-hello

check "sysext .raw source content intact" \
    grep -q 'E2E_SYSEXT_RAW_OK' /usr/bin/sysext-raw-hello

check "confext directory source present at merged path /etc/e2e-conf/main.conf" \
    test -f /etc/e2e-conf/main.conf

check "confext directory source content intact" \
    grep -q 'E2E_CONF_MARKER=present' /etc/e2e-conf/main.conf

check "/var/lib/extensions exists (created by placement)" \
    test -d /var/lib/extensions

check "/var/lib/extensions is empty (bake removed artifacts)" \
    dir_empty /var/lib/extensions

check "/var/lib/confexts exists (created by placement)" \
    test -d /var/lib/confexts

check "/var/lib/confexts is empty (bake removed artifacts)" \
    dir_empty /var/lib/confexts

check "baked release file absent: /usr/lib/extension-release.d/extension-release.sysext-dir" \
    sh -c 'test ! -e /usr/lib/extension-release.d/extension-release.sysext-dir'

check "baked release file absent: /usr/lib/extension-release.d/extension-release.sysext-raw" \
    sh -c 'test ! -e /usr/lib/extension-release.d/extension-release.sysext-raw'

check "baked release file absent: /etc/extension-release.d/extension-release.e2e-conf" \
    sh -c 'test ! -e /etc/extension-release.d/extension-release.e2e-conf'

# Bake mode must not enable the boot services. Reported for the
# evidence trail but intentionally non-fatal: Ubuntu 24.04 ships these units
# enabled-by-default (systemd >= 254), so "enabled" here does not imply the
# plugin enabled them. Treat this as informational only.
printf 'INFO: systemd-sysext.service is-enabled: %s\n' \
    "$(systemctl is-enabled systemd-sysext.service 2>/dev/null || printf 'unknown')"
printf 'INFO: systemd-confext.service is-enabled: %s\n' \
    "$(systemctl is-enabled systemd-confext.service 2>/dev/null || printf 'unknown')"

printf 'INFO: systemd version: %s\n' \
    "$(systemctl --version | head -n1)"

if [ "$FAILURES" -gt 0 ]; then
    printf 'RESULT: FAIL (%d assertion(s) failed)\n' "$FAILURES" >&2
    exit 1
fi
printf 'RESULT: ALL E2E BAKE ASSERTIONS PASSED\n'
