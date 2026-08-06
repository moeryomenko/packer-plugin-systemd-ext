#!/usr/bin/env bash
# E2E bake acceptance runner, cloud-hypervisor builder.
#
# Full pipeline:
#   1. fail-fast host prerequisite aggregate (KVM, CH >= 38, packer >= 1.9,
#      qemu-img, go >= 1.26, TAP/root) via test/e2e/prereq/cmd/prereq
#   2. build the systemd-ext plugin (make build) and install it via
#      `packer plugins install --path` (registration proof)
#   3. build + install the cloud-hypervisor builder plugin from the pinned
#      ref CH_PLUGIN_REF (sibling checkout first, else clone at that SHA),
#      with a minimal local fix applied to a private copy for the pinned
#      ref's SSH communicator-validation ordering bug (see plugin section)
#   4. fetch the pinned EDK2 firmware (CLOUDHV.fd) and Ubuntu 24.04 cloud
#      image (qcow2) with pinned sha256 checksums; convert the image to raw
#      with qemu-img convert
#   5. create the TAP device ch-tap-0 (sudo ip tuntap; idempotent), assign
#      the host-side gateway address, and register best-effort cleanup
#   6. generate the prebuilt .raw fixture with mksquashfs, including the
#      mandatory in-image release file matching the pinned guest
#   7. generate the cloud-init NoCloud seed (meta-data/user-data/network-config)
#      and pack it into a cidata ISO for the readonly seed disk
#   8. packer validate (always) - template + provisioner schema gate
#   9. packer build template.pkr.hcl; the guest-side assert.sh runs inside
#      the build and fails the build on any failed assertion
#  10. report PASS / FAIL (the build log must carry assert.sh's PASS marker;
#      a build with a silent assertion failure is never green)
#
# Modes:
#   default         full cloud-hypervisor build (requires /dev/kvm)
#   VALIDATE_ONLY=1 prereq/plugins/assets/TAP/fixtures/seed + packer validate,
#                   no build
#   SKIP_E2E=1      explicit skip; prints a loud SKIPPED banner. This is the
#                   only sanctioned "skip" path: the suite is never silently
#                   false-green - skipping is opt-in and visible, every other
#                   failure mode exits non-zero.
#
# Environment overrides (all optional):
#   E2E_FIRMWARE_URL / E2E_FIRMWARE_SHA256   override the pinned EDK2 asset
#   E2E_IMAGE_URL / E2E_IMAGE_SHA256         override the pinned Ubuntu image
#   E2E_RAW_ID / E2E_RAW_VERSION             os-release baked into the .raw
#                                            fixture (defaults ubuntu/24.04)
#   E2E_GUEST_IP / E2E_TAP_HOST_IP                 network overrides (the TAP
#                                                  name is the literal
#                                                  ch-tap-0)
#   E2E_CH_PLUGIN_REF                       bump the cloud-hypervisor pin
#   PACKER_BIN                              packer binary (default: packer)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${ROOT}/../../.." && pwd)"
OUT="${ROOT}/.out"
SEED_DIR="${OUT}/seed"
PREBUILT_DIR="${OUT}/prebuilt"
RAW_NAME="sysext-raw"
RAW_RELEASE_ID="${E2E_RAW_ID:-ubuntu}"
RAW_RELEASE_VERSION="${E2E_RAW_VERSION:-24.04}"
PLUGIN_SOURCE="$(grep -E '^module ' "${REPO_ROOT}/go.mod" | awk '{print $2}' | sed 's/packer-plugin-//')"

# Pinned assets. The Ubuntu sha256
# was verified against .../releases/24.04/release/SHA256SUMS on 2026-08-05;
# run.sh re-checks it against the live SHA256SUMS each run and warns on drift.
FIRMWARE_URL="${E2E_FIRMWARE_URL:-https://github.com/cloud-hypervisor/edk2/releases/download/ch-1e1b96f126/CLOUDHV.fd}"
FIRMWARE_SHA256="${E2E_FIRMWARE_SHA256:-9fb511fc0dd423d90a79615a90a8ace9b9e078b4a115ea2c459e0ac2f4e60218}"
IMAGE_URL="${E2E_IMAGE_URL:-https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img}"
IMAGE_SHA256="${E2E_IMAGE_SHA256:-0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe}"
IMAGE_SHA256SUMS_URL="https://cloud-images.ubuntu.com/releases/24.04/release/SHA256SUMS"

# systemd-dissect for the guest (.raw bake): the base Ubuntu 24.04
# cloud image does not ship the binary (only a bash-completion stub; the
# binary is in systemd-container). run.sh extracts /usr/bin/systemd-dissect
# from the pinned systemd-container .deb matching the guest's systemd
# (255.4-1ubuntu8.16) and the bake template uploads it into the guest via the
# file provisioner. The .deb sha256 was verified against the archive on
# 2026-08-06; the binary links libsystemd-shared-255.so + libc.so.6, both
# present in the guest.
DISSECT_DEB_URL="${E2E_DISSECT_DEB_URL:-https://archive.ubuntu.com/ubuntu/pool/main/s/systemd/systemd-container_255.4-1ubuntu8.16_amd64.deb}"
DISSECT_DEB_SHA256="${E2E_DISSECT_DEB_SHA256:-7ab146ac98bf9c6095935b37a906d589795105c301626f2fd5db6d0e77bd9472}"

# Cloud-hypervisor plugin pin.
CH_PLUGIN_REF="${E2E_CH_PLUGIN_REF:-979d702db885417820fa27fbcfce42256c90e110}"
CH_PLUGIN_REPO_URL="https://github.com/moeryomenko/packer-plugin-cloud-hypervisor.git"
CH_SIBLING_DIR="/home/eryoma/workspace/packer-plugin-cloud-hypervisor"

# Networking: host side of the TAP is the gateway; the guest gets the static
# address the plugin uses as the SSH host (CommHost reads network_interfaces.ip).
# The TAP name is the literal ch-tap-0 (the template hardcodes it).
# The template's network_interfaces.ip must be a plain address (no /24 suffix):
# the cloud-hypervisor API deserializes net[].ip as IpAddr and rejects CIDR
# syntax, and requires the separate dotted-quad network_interfaces.mask at
# vm.boot (an ip without a mask is rejected). TAP_NETMASK is the /24 prefix
# used by the cloud-init seed and the host TAP address; TAP_MASK is the same
# mask in dotted-quad form for the template variable.
TAP_DEVICE="ch-tap-0"
TAP_HOST_IP="${E2E_TAP_HOST_IP:-10.0.2.1}"
GUEST_IP="${E2E_GUEST_IP:-10.0.2.2}"
TAP_NETMASK=24
TAP_MASK="255.255.255.0"

# ---------------------------------------------------------------------------
# skip path (explicit and loud)
# ---------------------------------------------------------------------------
if [ -n "${SKIP_E2E:-}" ]; then
    printf '\n'
    printf '############################################################\n'
    printf '# E2E BAKE SUITE SKIPPED (SKIP_E2E is set)                 #\n'
    printf '# NO ASSERTIONS EXECUTED - this is not a pass              #\n'
    printf '############################################################\n'
    printf '\n'
    exit 0
fi

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    sed -n '2,40p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 0
fi

# ---------------------------------------------------------------------------
# helpers
# ---------------------------------------------------------------------------
fail() { printf 'E2E BAKE FAIL: %s\n' "$*" >&2; exit 1; }

PACKER_BIN="${PACKER_BIN:-packer}"
mkdir -p "${OUT}" "${SEED_DIR}" "${PREBUILT_DIR}"

# best-effort cleanup of host-level and temp resources on any exit path
CREATED_TAP=0
cleanup() {
    if [ "${CREATED_TAP}" = "1" ]; then
        printf 'cleaning up TAP device %s\n' "${TAP_DEVICE}"
        sudo ip link del dev "${TAP_DEVICE}" 2>/dev/null || true
        CREATED_TAP=0
    fi
    rm -rf "${PLUGIN_DIR:-}"
}
trap cleanup EXIT

# fetch_pinned <url> <dest> <expected_sha256> <desc>: download an asset and
# verify it against the pinned checksum (hard pin: mismatch fails loudly).
fetch_pinned() {
    local url="$1" dest="$2" expected="$3" desc="$4" got=""
    if [ -f "${dest}" ]; then
        got="$(sha256sum "${dest}" | awk '{print $1}')"
        if [ "${got}" = "${expected}" ]; then
            printf 'asset ok (cached): %s\n' "${desc}"
            return 0
        fi
        printf 'asset stale (%s): re-fetching %s\n' "${desc}" "$(basename "${dest}")"
    fi
    curl -fsSL -o "${dest}" "${url}" || fail "download failed for ${desc} (${url})"
    got="$(sha256sum "${dest}" | awk '{print $1}')"
    if [ "${got}" != "${expected}" ]; then
        fail "checksum mismatch for ${desc}: got ${got}, want ${expected}"
    fi
    printf 'asset ok: %s (sha256 %s)\n' "${desc}" "${got}"
}

# make_seed_iso: pack .out/seed/ into an ISO9660 volume labeled "cidata" so
# cloud-init NoCloud in the guest consumes it.
make_seed_iso() {
    local tool=""
    if command -v cloud-localds >/dev/null 2>&1; then
        tool="cloud-localds"
    elif command -v genisoimage >/dev/null 2>&1; then
        tool="genisoimage"
    elif command -v xorriso >/dev/null 2>&1; then
        tool="xorriso"
    else
        fail "no cloud-init seed ISO tool found: install cloud-localds, genisoimage, or xorriso"
    fi
    case "${tool}" in
        cloud-localds)
            cloud-localds --network-config "${SEED_DIR}/network-config" \
                -f "${OUT}/seed.iso" "${SEED_DIR}/user-data" "${SEED_DIR}/meta-data" \
                || fail "cloud-localds failed"
            ;;
        genisoimage)
            genisoimage -quiet -output "${OUT}/seed.iso" -volid cidata -joliet -rock \
                "${SEED_DIR}" || fail "genisoimage failed"
            ;;
        xorriso)
            xorriso -as mkisofs -quiet -output "${OUT}/seed.iso" -volid cidata -joliet -rock \
                "${SEED_DIR}" || fail "xorriso failed"
            ;;
    esac
}

# ---------------------------------------------------------------------------
# 1. fail-fast prerequisites
# ---------------------------------------------------------------------------
printf '\n== [1/8] host prerequisites ==\n'
(cd "${REPO_ROOT}" && go run ./test/e2e/prereq/cmd/prereq) \
    || fail "prerequisite aggregate failed (see message above)"

for cmd in ip curl sha256sum mksquashfs unsquashfs ar tar zstd ssh-keygen; do
    command -v "${cmd}" >/dev/null 2>&1 \
        || fail "required host tool '${cmd}' not found on PATH (see README.md prerequisites)"
done

printf 'E2E bake suite: root=%s\n' "${ROOT}"
printf 'E2E bake suite: builder=cloud-hypervisor plugin=%s ref=%s\n' \
    "${PLUGIN_SOURCE}" "${CH_PLUGIN_REF}"

# ---------------------------------------------------------------------------
# 2. build + install plugins (hermetic PACKER_PLUGIN_PATH)
# ---------------------------------------------------------------------------
printf '\n== [2/8] building + installing plugins ==\n'
make -C "${REPO_ROOT}" build || fail "make build failed"

PLUGIN_DIR="$(mktemp -d)"
export PACKER_PLUGIN_PATH="${PLUGIN_DIR}"

# the plugin under test, from the just-built binary (packer accepts
# the sysext/confext provisioner blocks)
"${PACKER_BIN}" plugins install --path "${REPO_ROOT}/packer-plugin-systemd-ext" "${PLUGIN_SOURCE}" \
    || fail "packer plugins install --path failed for ${PLUGIN_SOURCE}"

# the cloud-hypervisor builder plugin from the pinned ref:
# sibling checkout first, otherwise clone at CH_PLUGIN_REF. Build from a
# PRIVATE copy under .out/ so the sibling checkout is never modified.
if [ -d "${CH_SIBLING_DIR}" ] \
    && [ "$(git -C "${CH_SIBLING_DIR}" rev-parse HEAD 2>/dev/null)" = "${CH_PLUGIN_REF}" ]; then
    CH_PLUGIN_ORIGIN="${CH_SIBLING_DIR}"
    printf 'cloud-hypervisor plugin source: sibling checkout %s (pinned ref)\n' "${CH_SIBLING_DIR}"
else
    CH_PLUGIN_ORIGIN="${CH_PLUGIN_REPO_URL}"
fi
CH_PLUGIN_BUILD_DIR="${OUT}/ch-plugin-src"
rm -rf "${CH_PLUGIN_BUILD_DIR}"
git clone --quiet "${CH_PLUGIN_ORIGIN}" "${CH_PLUGIN_BUILD_DIR}" \
    || fail "git clone of packer-plugin-cloud-hypervisor failed"
git -C "${CH_PLUGIN_BUILD_DIR}" checkout --quiet "${CH_PLUGIN_REF}" \
    || fail "git checkout ${CH_PLUGIN_REF} failed (does the clone contain the pinned ref?)"

# The pinned ref 979d702 validates the SSH communicator BEFORE decoding the
# template config (builder/cloud-hypervisor/config.go Prepare order), so every
# SSH build fails with "An ssh_username must be specified"; the plugin's own
# examples only use communicator="none" and its tests never exercise a passing
# SSH prepare. Apply a minimal, deterministic reorder fix (config.Decode
# before CommConfig.Prepare) to the private copy only. If a future pin fixes
# the ordering, the guard below skips the patch.
if awk '/c\.CommConfig\.Prepare\(/{p=NR} /err := config\.Decode\(/{d=NR} END{exit !(p && d && p < d)}' \
        "${CH_PLUGIN_BUILD_DIR}/builder/cloud-hypervisor/config.go"; then
    printf 'cloud-hypervisor plugin %s has the communicator-validation ordering bug; applying local fix\n' "${CH_PLUGIN_REF}"
    git -C "${CH_PLUGIN_BUILD_DIR}" apply - <<'PATCH' \
        || fail "applying the pinned cloud-hypervisor communicator fix failed (update the patch in run.sh)"
diff --git a/builder/cloud-hypervisor/config.go b/builder/cloud-hypervisor/config.go
index a86e13d..45a021d 100644
--- a/builder/cloud-hypervisor/config.go
+++ b/builder/cloud-hypervisor/config.go
@@ -149,6 +149,15 @@ func (c *Config) Prepare(raws ...interface{}) ([]string, error) {
 	c.Serial = defaultSerialMode
 	c.Console = defaultSerialMode
 
+	err := config.Decode(c, &config.DecodeOpts{
+		PluginType:        BuilderID,
+		Interpolate:       true,
+		InterpolateFilter: &interpolate.RenderFilter{},
+	}, raws...)
+	if err != nil {
+		return nil, fmt.Errorf("config decode: %w", err)
+	}
+
 	// Initialize communicator defaults (SSHPort=22, etc.).
 	// This also validates SSH key file existence, host key settings, etc.
 	if errs := c.CommConfig.Prepare(&interpolate.Context{}); len(errs) > 0 {
@@ -159,15 +168,6 @@ func (c *Config) Prepare(raws ...interface{}) ([]string, error) {
 		return nil, fmt.Errorf("communicator config: %s", strings.Join(msgs, "; "))
 	}
 
-	err := config.Decode(c, &config.DecodeOpts{
-		PluginType:        BuilderID,
-		Interpolate:       true,
-		InterpolateFilter: &interpolate.RenderFilter{},
-	}, raws...)
-	if err != nil {
-		return nil, fmt.Errorf("config decode: %w", err)
-	}
-
 	var warnings []string
 	var errs *packersdk.MultiError

PATCH
fi

( cd "${CH_PLUGIN_BUILD_DIR}" && go build -o "${OUT}/packer-plugin-cloud-hypervisor" . ) \
    || fail "building packer-plugin-cloud-hypervisor at ${CH_PLUGIN_REF} failed"
"${PACKER_BIN}" plugins install --path "${OUT}/packer-plugin-cloud-hypervisor" \
    "github.com/moeryomenko/cloud-hypervisor" \
    || fail "packer plugins install --path failed for cloud-hypervisor"

# ---------------------------------------------------------------------------
# 3. assets: pinned EDK2 firmware + Ubuntu 24.04 image, qcow2 -> raw
# ---------------------------------------------------------------------------
printf '\n== [3/8] fetching pinned assets ==\n'
FIRMWARE_PATH="${OUT}/CLOUDHV.fd"
IMAGE_QCOW2="${OUT}/ubuntu-24.04-server-cloudimg-amd64.img"
DISK_RAW="${OUT}/ubuntu-24.04-server-cloudimg-amd64.raw"

fetch_pinned "${FIRMWARE_URL}" "${FIRMWARE_PATH}" "${FIRMWARE_SHA256}" "EDK2 CLOUDHV.fd firmware"
fetch_pinned "${IMAGE_URL}" "${IMAGE_QCOW2}" "${IMAGE_SHA256}" "Ubuntu 24.04 cloud image"

# SHA256SUMS fetch pattern: confirm the hard pin still matches what the
# release directory publishes; drift is a loud warning, never silent.
if curl -fsSL -o "${OUT}/SHA256SUMS" "${IMAGE_SHA256SUMS_URL}" 2>/dev/null; then
    published="$(awk '{print $1; exit}' <(grep -E 'ubuntu-24.04-server-cloudimg-amd64\.img$' "${OUT}/SHA256SUMS"))"
    if [ -n "${published}" ] && [ "${published}" != "${IMAGE_SHA256}" ]; then
        printf 'WARN: published SHA256SUMS hash %s differs from pin %s; re-pin deliberately\n' \
            "${published}" "${IMAGE_SHA256}" >&2
    fi
fi

if [ ! -f "${DISK_RAW}" ] || [ "${IMAGE_QCOW2}" -nt "${DISK_RAW}" ]; then
    printf 'converting %s -> %s\n' "$(basename "${IMAGE_QCOW2}")" "$(basename "${DISK_RAW}")"
    qemu-img convert -O raw "${IMAGE_QCOW2}" "${DISK_RAW}" \
        || fail "qemu-img convert -O raw failed"
fi
printf 'raw disk ready: %s\n' "${DISK_RAW}"

# ---------------------------------------------------------------------------
# 4. TAP device (idempotent) + host-side gateway + best-effort cleanup
# ---------------------------------------------------------------------------
printf '\n== [4/8] TAP device ==\n'
if ip link show "${TAP_DEVICE}" >/dev/null 2>&1; then
    printf 'tap %s already exists; reusing\n' "${TAP_DEVICE}"
else
    sudo ip tuntap add dev "${TAP_DEVICE}" mode tap \
        || fail "cannot create TAP ${TAP_DEVICE} (requires root/sudo)"
    CREATED_TAP=1
fi
if ! ip addr show dev "${TAP_DEVICE}" | grep -q "${TAP_HOST_IP}/"; then
    sudo ip addr add "${TAP_HOST_IP}/${TAP_NETMASK}" dev "${TAP_DEVICE}" \
        || fail "cannot assign ${TAP_HOST_IP}/${TAP_NETMASK} to ${TAP_DEVICE}"
fi
sudo ip link set "${TAP_DEVICE}" up || fail "cannot bring ${TAP_DEVICE} up"

# ---------------------------------------------------------------------------
# 5. fixtures: prebuilt .raw (squashfs) with a matching in-image release file
# ---------------------------------------------------------------------------
printf '\n== [5/8] generating fixtures ==\n'
RAW_TREE="${OUT}/raw-tree"
rm -rf "${RAW_TREE}" "${PREBUILT_DIR}/${RAW_NAME}.raw"
mkdir -p "${RAW_TREE}/usr/bin" "${RAW_TREE}/usr/lib/extension-release.d"

cat > "${RAW_TREE}/usr/bin/sysext-raw-hello" <<'EOF'
#!/bin/sh
printf '%s\n' 'E2E_SYSEXT_RAW_OK'
EOF
chmod 0755 "${RAW_TREE}/usr/bin/sysext-raw-hello"

# A .raw source must carry its own release file, and its
# ID/VERSION_ID must match the pinned guest or the in-guest merge rejects it.
cat > "${RAW_TREE}/usr/lib/extension-release.d/extension-release.${RAW_NAME}" <<EOF
ID=${RAW_RELEASE_ID}
VERSION_ID=${RAW_RELEASE_VERSION}
EOF

mksquashfs "${RAW_TREE}" "${PREBUILT_DIR}/${RAW_NAME}.raw" >/dev/null 2>&1 \
    || fail "mksquashfs failed generating ${RAW_NAME}.raw (squashfs-tools missing or broken)"

# fast local sanity: the image must carry the expected tree. The authoritative
# content check is the in-guest systemd-dissect --copy-from during the build.
if unsquashfs -l "${PREBUILT_DIR}/${RAW_NAME}.raw" 2>/dev/null | grep -q "extension-release.${RAW_NAME}"; then
    printf 'fixture ok: %s contains usr/lib/extension-release.d/extension-release.%s\n' \
        "${RAW_NAME}.raw" "${RAW_NAME}"
else
    fail "unsquashfs sanity check failed on ${RAW_NAME}.raw"
fi

# The guest needs /usr/bin/systemd-dissect for the .raw bake, but
# the base Ubuntu 24.04 cloud image does not ship it. Fetch the pinned
# systemd-container .deb (same checksum-pinned pattern as the other assets)
# and extract the binary; the bake template uploads it into the guest with a
# file provisioner before any extension provisioner runs.
DISSECT_DEB="${OUT}/systemd-container.deb"
DISSECT_BIN="${OUT}/systemd-dissect"
DISSECT_XDIR="${OUT}/dissect-x"
fetch_pinned "${DISSECT_DEB_URL}" "${DISSECT_DEB}" "${DISSECT_DEB_SHA256}" "Ubuntu 24.04 systemd-container (systemd-dissect)"
if [ ! -f "${DISSECT_BIN}" ] || [ "${DISSECT_DEB}" -nt "${DISSECT_BIN}" ]; then
    rm -rf "${DISSECT_XDIR}"
    mkdir -p "${DISSECT_XDIR}"
    ( cd "${DISSECT_XDIR}" && ar x "${DISSECT_DEB}" && tar --zstd -xf data.tar.zst ./usr/bin/systemd-dissect ) \
        || fail "extracting systemd-dissect from ${DISSECT_DEB} failed"
    cp "${DISSECT_XDIR}/usr/bin/systemd-dissect" "${DISSECT_BIN}" \
        || fail "copying extracted systemd-dissect to ${DISSECT_BIN} failed"
    chmod 0755 "${DISSECT_BIN}"
fi
printf 'systemd-dissect for guest ready: %s\n' "${DISSECT_BIN}"

# ---------------------------------------------------------------------------
# 6. cloud-init NoCloud seed (static guest IP + ubuntu/uid-0 user data)
# ---------------------------------------------------------------------------
printf '\n== [6/8] generating cloud-init seed ==\n'
cat > "${SEED_DIR}/meta-data" <<'EOF'
instance-id: packer-bake-e2e
local-hostname: bake-e2e
EOF

# The plugin preflight requires uid 0 and the packer SSH
# communicator has no sudo wrapper, so the seed makes the ubuntu user uid 0
# before sshd accepts connections (cloud-init `bootcmd` runs in the init
# stage, before the config stage's cc_users creates the default ubuntu user;
# `runcmd` would be too late and the preflight would see uid 1000). On a
# first boot ubuntu does not exist yet, so bootcmd creates it directly with
# useradd -o -u 0; on later boots cc_users already created it, so bootcmd
# re-ids it with usermod -o -u 0.
#
# SSH uses ephemeral PUBLICKEY auth, not passwords: run.sh generates
# .out/e2e-ssh-key and bootcmd installs the public key into
# /home/ubuntu/.ssh/authorized_keys before sshd accepts connections. Password
# auth is not usable on a fresh first boot of this image: sshd/PAM rejects
# even a correctly hashed ubuntu/ubuntu password (verified in the harness E2E
# via chroot crypt(3) MATCH plus live sshpass rejection), while publickey
# auth succeeds. The bake template therefore sets ssh_private_key_file and no
# ssh_password (the ssh_username literal stays "ubuntu").
#
# bootcmd also pre-creates the standard sysext/confext install directories
# (/var/lib/extensions, /var/lib/confexts): a stock Ubuntu cloud image has
# neither, and the provisioner's atomic placement (`mv staging
# installDir/<name>.tmp`) fails with ENOENT when the destination parent is
# missing (verified against the fresh guest; the assertions also check the dirs
# exist and are empty after the bake).
SSH_KEY="${OUT}/e2e-ssh-key"
if [ ! -f "${SSH_KEY}" ]; then
    ssh-keygen -q -t ed25519 -N "" -f "${SSH_KEY}" \
        || fail "ssh-keygen failed generating the ephemeral SSH key"
fi
E2E_SSH_PUBKEY="$(cat "${SSH_KEY}.pub")"

cat > "${SEED_DIR}/user-data" <<'EOF'
#cloud-config
bootcmd:
  - [sh, -c, 'id ubuntu >/dev/null 2>&1 && usermod -o -u 0 ubuntu || useradd -o -u 0 -m -s /bin/bash ubuntu']
  - [sh, -c, 'mkdir -p /home/ubuntu/.ssh && echo "__E2E_SSH_PUBKEY__" > /home/ubuntu/.ssh/authorized_keys && chown -R ubuntu:ubuntu /home/ubuntu/.ssh && chmod 700 /home/ubuntu/.ssh && chmod 600 /home/ubuntu/.ssh/authorized_keys']
  - [mkdir, -p, /var/lib/extensions, /var/lib/confexts]
EOF
sed -i "s|__E2E_SSH_PUBKEY__|${E2E_SSH_PUBKEY}|" "${SEED_DIR}/user-data"

cat > "${SEED_DIR}/network-config" <<EOF
version: 2
ethernets:
  id0:
    match:
      driver: virtio_net
    addresses:
      - ${GUEST_IP}/${TAP_NETMASK}
    routes:
      - to: default
        via: ${TAP_HOST_IP}
    nameservers:
      addresses: [${TAP_HOST_IP}]
EOF

make_seed_iso
printf 'seed iso ready: %s\n' "${OUT}/seed.iso"

# ---------------------------------------------------------------------------
# 7. validate (always) then build
# ---------------------------------------------------------------------------
BUILD_ARGS=()
# guest_ip must be a plain address (no /24 suffix): the template's
# network_interfaces.ip is the SSH host (CommHost), and the cloud-hypervisor
# API deserializes net[].ip as IpAddr, rejecting CIDR syntax. The mask is a
# separate dotted-quad variable (CH rejects an ip without a mask at vm.boot).
# The ephemeral SSH private key authenticates the provisioner (the template
# defaults to .out/e2e-ssh-key, so this is only needed for an override).
[ -n "${E2E_GUEST_IP:-}" ] && BUILD_ARGS+=(
    -var "guest_ip=${E2E_GUEST_IP}"
    -var "guest_mask=${TAP_MASK}"
)
BUILD_ARGS+=(-var "ssh_private_key=${SSH_KEY}")

printf '\n== [7/8] packer validate ==\n'
"${PACKER_BIN}" validate "${BUILD_ARGS[@]}" "${ROOT}/template.pkr.hcl" \
    2>&1 | tee "${OUT}/validate.log" \
    || fail "packer validate failed (see ${OUT}/validate.log)"

if [ -n "${VALIDATE_ONLY:-}" ]; then
    printf '\nE2E BAKE RESULT: VALIDATE PASS (VALIDATE_ONLY=1; cloud-hypervisor build not run)\n'
    exit 0
fi

printf '\n== [8/8] packer build (cloud-hypervisor firmware boot, systemd >= 255 guest) ==\n'
printf 'image checksum pin: %s\n' "${IMAGE_SHA256}"
"${PACKER_BIN}" build "${BUILD_ARGS[@]}" "${ROOT}/template.pkr.hcl" \
    | tee "${OUT}/build.log"

RC=${PIPESTATUS[0]}
if [ "${RC}" -ne 0 ]; then
    printf '\nE2E BAKE FAIL: packer build exited %d (full log: %s)\n' "${RC}" "${OUT}/build.log" >&2
    exit 1
fi

# assert.sh runs inside the build as the shell provisioner; a failed assertion
# already fails the build, and the PASS marker below must be visible in the
# log - a build that silently skipped the assertions is never green.
if ! grep -q 'RESULT: ALL E2E BAKE ASSERTIONS PASSED' "${OUT}/build.log"; then
    printf '\nE2E BAKE FAIL: build succeeded but assert.sh PASS marker missing from %s\n' \
        "${OUT}/build.log" >&2
    exit 1
fi

printf '\nE2E BAKE RESULT: PASS (guest assertions ran inside the build; see %s)\n' "${OUT}/build.log"
