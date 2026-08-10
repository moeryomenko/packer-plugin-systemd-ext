#!/usr/bin/env bash
# E2E persist acceptance runner.
#
# Full pipeline:
#   1. fail-fast host prerequisites: test/e2e/prereq aggregate (kvm,
#      cloud-hypervisor >= 38, packer >= 1.9, qemu-img, go >= 1.26,
#      tap/root) plus the harness tools (curl, sha256sum, mtools, ip, sudo)
#   2. build the systemd-ext plugin (make build) and install it via
#      `packer plugins install --path` into a hermetic PACKER_PLUGIN_PATH,
#      together with the pinned cloud-hypervisor builder plugin
#      (CH_PLUGIN_REF, built from a private copy of the sibling checkout or a
#      clone at that SHA, with a minimal local fix for the pinned ref's
#      communicator-validation ordering bug - see the plugin-build section)
#   3. fetch pinned assets (EDK2 CLOUDHV.fd + Ubuntu 24.04 cloud image) with
#      checksum verification; qemu-img convert qcow2 -> raw
#   4. build a cloud-init NoCloud seed disk (mtools, vfat, label "cidata")
#      that installs the ephemeral SSH public key, re-ids ubuntu to uid 0
#      (the plugin preflight requires root; the packer SSH
#      communicator has no sudo wrapper), pre-creates the extension install
#      dirs, and pins the guest static IP
#      192.168.249.2/24; create TAP ch-tap-0 (idempotent, sudo); grant
#      cloud-hypervisor CAP_NET_ADMIN for TAP attach
#   5. packer validate (always) both templates
#   6. packer build template.pkr.hcl (build 1); the guest-side assert.sh runs
#      inside the build and fails the build on any failed assertion
#   7. resolve the build-1 artifact from output-persist/ (the cloud-hypervisor
#      builder copies writable disks to output-<buildname>/) and run
#      packer build template-second-boot.pkr.hcl (build 2) with the artifact
#      as -var artifact_disk; assert-second-boot.sh runs inside build 2
#   8. host-side post-build verification + report PASS / FAIL
#
# Modes:
#   default         full cloud-hypervisor builds (requires KVM, TAP/root,
#                   qemu-img, CH >= 38; see README.md prerequisites)
#   VALIDATE_ONLY=1 build/install/assets/seed/tap + packer validate, no builds
#   SKIP_E2E=1      explicit skip; prints a loud SKIPPED banner. This is the
#                   only sanctioned "skip" path (plan A2): the suite is
#                   never silently false-green - skipping is opt-in and
#                   visible, every other failure mode exits non-zero.
#
# Environment overrides (all optional):
#   E2E_IMAGE_URL        override the pinned image URL
#   E2E_IMAGE_CHECKSUM   override the pinned image checksum
#   CH_PLUGIN_REF        pinned cloud-hypervisor plugin commit (default below)
#   CH_PLUGIN_SRC        sibling checkout of the plugin
#                        (default ${HOME}/workspace/packer-plugin-cloud-hypervisor)
#   PACKER_BIN           packer binary (default: packer on PATH)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${ROOT}/../../.." && pwd)"
OUT="${ROOT}/.out"
ASSETS_DIR="${OUT}/assets"
SEED_DIR="${OUT}/seed"
SEED_IMG="${OUT}/seed.raw"
PLUGIN_SOURCE="$(grep -E '^module ' "${REPO_ROOT}/go.mod" | awk '{print $2}' | sed 's/packer-plugin-//')"

# Pinned plugin ref and assets.
CH_PLUGIN_REF="${CH_PLUGIN_REF:-979d702db885417820fa27fbcfce42256c90e110}"
CH_PLUGIN_SRC="${CH_PLUGIN_SRC:-${HOME}/workspace/packer-plugin-cloud-hypervisor}"
# Full plugin source for `packer plugins install` (host/path, derived from the
# pinned plugin's module path without the packer-plugin- prefix).
CH_PLUGIN_SOURCE="github.com/moeryomenko/cloud-hypervisor"
EDK2_URL="https://github.com/cloud-hypervisor/edk2/releases/download/ch-1e1b96f126/CLOUDHV.fd"
EDK2_SHA256="9fb511fc0dd423d90a79615a90a8ace9b9e078b4a115ea2c459e0ac2f4e60218"
IMAGE_NAME="ubuntu-24.04-server-cloudimg-amd64"
IMAGE_URL="${E2E_IMAGE_URL:-https://cloud-images.ubuntu.com/releases/24.04/release/${IMAGE_NAME}.img}"
IMAGE_SHA256="${E2E_IMAGE_CHECKSUM:-0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe}"

TAP_DEVICE="ch-tap-0"
GUEST_IP="192.168.249.2"
HOST_IP="192.168.249.1"
NETMASK="24"

TAP_CREATED=0

fail() { printf 'E2E PERSIST FAIL: %s\n' "$*" >&2; exit 1; }

cleanup() {
    if [ "${TAP_CREATED}" = "1" ] && ip link show "${TAP_DEVICE}" >/dev/null 2>&1; then
        sudo ip link del "${TAP_DEVICE}" 2>/dev/null || true
    fi
}
trap cleanup EXIT

# ---------------------------------------------------------------------------
# skip path (explicit and loud)
# ---------------------------------------------------------------------------
if [ -n "${SKIP_E2E:-}" ]; then
    printf '\n'
    printf '############################################################\n'
    printf '# E2E PERSIST SUITE SKIPPED (SKIP_E2E is set)              #\n'
    printf '# NO ASSERTIONS EXECUTED - this is not a pass              #\n'
    printf '############################################################\n'
    printf '\n'
    exit 0
fi

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
    sed -n '2,50p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'
    exit 0
fi

# ---------------------------------------------------------------------------
# 1. prerequisites
# ---------------------------------------------------------------------------
PACKER_BIN="${PACKER_BIN:-packer}"

printf 'E2E persist suite: root=%s\n' "${ROOT}"
printf 'E2E persist suite: plugin=%s ch-ref=%s tap=%s guest-ip=%s\n' \
    "${PLUGIN_SOURCE}" "${CH_PLUGIN_REF}" "${TAP_DEVICE}" "${GUEST_IP}"
mkdir -p "${OUT}" "${ASSETS_DIR}" "${SEED_DIR}"

# The fail-fast prerequisite aggregate (actionable messages naming the missing
# item). The prereq checker lives in test/e2e/prereq/cmd/prereq; this runner
# only consumes it, so `make e2e-persist` works once that entry exists.
printf '\n== [1/8] host prerequisites ==\n'
( cd "${REPO_ROOT}" && go run ./test/e2e/prereq/cmd/prereq ) \
    || fail "host prerequisite check failed (see the checker output above)"

for cmd in curl sha256sum mformat mcopy ip ssh-keygen; do
    command -v "${cmd}" >/dev/null 2>&1 \
        || fail "required host tool '${cmd}' not found on PATH (see README.md prerequisites)"
done
if [ "$(id -u)" != "0" ]; then
    command -v sudo >/dev/null 2>&1 \
        || fail "required host tool 'sudo' not found on PATH (needed for TAP creation and setcap)"
fi

# ---------------------------------------------------------------------------
# 2. build + install plugins (hermetic PACKER_PLUGIN_PATH)
# ---------------------------------------------------------------------------
printf '\n== [2/8] building and installing plugins ==\n'
make -C "${REPO_ROOT}" build || fail "make build failed"

PLUGIN_DIR="$(mktemp -d)"
export PACKER_PLUGIN_PATH="${PLUGIN_DIR}"

"${PACKER_BIN}" plugins install --path "${REPO_ROOT}/packer-plugin-systemd-ext" "${PLUGIN_SOURCE}" \
    || fail "packer plugins install --path failed for ${PLUGIN_SOURCE}"

# cloud-hypervisor builder plugin from the pinned ref: sibling checkout
# first, else clone at that SHA. Build from a
# PRIVATE copy under .out/ so the sibling checkout is never modified.
if [ -d "${CH_PLUGIN_SRC}/.git" ]; then
    CH_PLUGIN_ORIGIN="${CH_PLUGIN_SRC}"
else
    CH_PLUGIN_ORIGIN="https://github.com/moeryomenko/packer-plugin-cloud-hypervisor.git"
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
"${PACKER_BIN}" plugins install --path "${OUT}/packer-plugin-cloud-hypervisor" "${CH_PLUGIN_SOURCE}" \
    || fail "packer plugins install --path failed for ${CH_PLUGIN_SOURCE}"

# ---------------------------------------------------------------------------
# 3. assets: EDK2 firmware + Ubuntu 24.04 cloud image (pinned checksums) +
#    qcow2 -> raw conversion
# ---------------------------------------------------------------------------
printf '\n== [3/8] fetching pinned assets ==\n'

fetch() { # fetch <url> <dest> <sha256> <desc>
    local url="$1" dest="$2" want="$3" desc="$4" got
    if [ -f "${dest}" ] && [ "$(sha256sum "${dest}" | awk '{print $1}')" = "${want}" ]; then
        printf 'asset ok (cached): %s\n' "${desc}"
        return 0
    fi
    printf 'downloading %s ...\n' "${desc}"
    curl -fsSL -o "${dest}.tmp" "${url}" || fail "download failed: ${url}"
    got="$(sha256sum "${dest}.tmp" | awk '{print $1}')"
    if [ "${got}" != "${want}" ]; then
        rm -f "${dest}.tmp"
        fail "checksum mismatch for ${desc}: got ${got}, want ${want}"
    fi
    mv "${dest}.tmp" "${dest}"
    printf 'asset ok: %s (%s)\n' "${desc}" "${want}"
}

FIRMWARE="${ASSETS_DIR}/CLOUDHV.fd"
fetch "${EDK2_URL}" "${FIRMWARE}" "${EDK2_SHA256}" "EDK2 Cloud Hypervisor firmware"

IMAGE_QCOW2="${ASSETS_DIR}/${IMAGE_NAME}.img"
fetch "${IMAGE_URL}" "${IMAGE_QCOW2}" "${IMAGE_SHA256}" "Ubuntu 24.04 cloud image"

RAW_DISK="${ASSETS_DIR}/${IMAGE_NAME}.raw"
# Fresh writable disk every run: persist mode mutates the disk by design
# (leaves extension images in /var/lib/extensions and /var/lib/confexts and
# enables systemd-sysext.service / systemd-confext.service). Reusing the
# previous run's raw carries that state: the enabled boot services auto-merge
# /usr at boot, so the provisioner's explicit merge fails with "Hierarchy
# '/usr' is already merged". Deleting the stale raw and re-converting restores
# the clean-first-boot conditions of the validated Aug 6 pass. The qcow2
# fetch+checksum cache above is untouched (that is not the problem).
rm -f "${RAW_DISK}"
printf 'converting cloud image to raw (qemu-img convert) ...\n'
qemu-img convert -O raw "${IMAGE_QCOW2}" "${RAW_DISK}" \
    || fail "qemu-img convert qcow2 -> raw failed"
printf 'raw disk: %s (fresh conversion this run)\n' "${RAW_DISK}"

# ---------------------------------------------------------------------------
# 4. cloud-init NoCloud seed + TAP + CH capability
# ---------------------------------------------------------------------------
printf '\n== [4/8] seed disk, TAP, CH capability ==\n'

# Per-run unique NoCloud instance-id: a constant id makes cloud-init treat the
# reused writable raw disk as the same instance (new=False) and never re-apply
# the seed network-config, so the guest never gets the static IP the packer SSH
# communicator needs (see README "Design notes").
INSTANCE_ID="packer-persist-e2e-$(date +%s)"
cat > "${SEED_DIR}/meta-data" <<EOF
instance-id: ${INSTANCE_ID}
local-hostname: persist-e2e
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
# auth succeeds. The templates therefore set ssh_private_key_file and no
# ssh_password (the ssh_username literal stays "ubuntu").
#
# bootcmd also pre-creates the standard sysext/confext install directories
# (/var/lib/extensions, /var/lib/confexts): a stock Ubuntu cloud image has
# neither, and the provisioner's atomic placement (`mv staging
# installDir/<name>.tmp`) fails with ENOENT when the destination parent is
# missing (the persist assertions also check the images remain in those dirs). network-config
# pins the guest to the static IP the template's network_interfaces.ip
# advertises (CommHost's SSH target). The seed disk is attached READ-ONLY to
# build 1 only; build 2 boots the provisioned artifact without a seed (the
# authorized key, the uid-0 ubuntu user and the install dirs are baked in).
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
      - ${GUEST_IP}/${NETMASK}
    dhcp4: false
EOF

rm -f "${SEED_IMG}"
mformat -C -i "${SEED_IMG}" -T 20480 -v cidata :: || fail "mformat failed (mtools required)"
mcopy -i "${SEED_IMG}" -o "${SEED_DIR}/user-data" ::user-data || fail "mcopy user-data failed"
mcopy -i "${SEED_IMG}" -o "${SEED_DIR}/meta-data" ::meta-data || fail "mcopy meta-data failed"
mcopy -i "${SEED_IMG}" -o "${SEED_DIR}/network-config" ::network-config || fail "mcopy network-config failed"
printf 'seed disk: %s (vfat, label cidata)\n' "${SEED_IMG}"

# TAP device (idempotent; requires root/sudo).
if ! ip link show "${TAP_DEVICE}" >/dev/null 2>&1; then
    sudo ip tuntap add dev "${TAP_DEVICE}" mode tap \
        || fail "failed to create TAP ${TAP_DEVICE} (requires root/sudo)"
    TAP_CREATED=1
fi
if ! ip addr show dev "${TAP_DEVICE}" 2>/dev/null | grep -q "${HOST_IP}/${NETMASK}"; then
    sudo ip addr add "${HOST_IP}/${NETMASK}" dev "${TAP_DEVICE}" \
        || fail "failed to assign ${HOST_IP}/${NETMASK} to ${TAP_DEVICE}"
fi
sudo ip link set "${TAP_DEVICE}" up || fail "failed to bring ${TAP_DEVICE} up"

# cloud-hypervisor needs CAP_NET_ADMIN to attach the existing TAP
# (TUNSETIFF). Grant it when the harness is not running as root.
if [ "$(id -u)" != "0" ]; then
    CH_BIN="$(command -v cloud-hypervisor)"
    sudo setcap cap_net_admin+ep "${CH_BIN}" \
        || fail "setcap cap_net_admin+ep on ${CH_BIN} failed: cloud-hypervisor cannot attach ${TAP_DEVICE} without CAP_NET_ADMIN"
fi

# ---------------------------------------------------------------------------
# 5. validate (always) then build 1
# ---------------------------------------------------------------------------
BUILD1_ARGS=(
    -var "firmware=${FIRMWARE}"
    -var "disk_path=${RAW_DISK}"
    -var "seed_disk=${SEED_IMG}"
    -var "ssh_private_key=${SSH_KEY}"
)

printf '\n== [5/8] packer validate (build 1 + build 2) ==\n'
"${PACKER_BIN}" validate "${BUILD1_ARGS[@]}" "${ROOT}/template.pkr.hcl" \
    2>&1 | tee "${OUT}/validate1.log" \
    || fail "packer validate failed for template.pkr.hcl (see ${OUT}/validate1.log)"

# Template 2's artifact disk is only known after build 1, but schema
# validation is identical for any regular file, so use the raw base disk in
# VALIDATE_ONLY mode and re-validate with the real artifact before build 2.
"${PACKER_BIN}" validate \
    -var "firmware=${FIRMWARE}" \
    -var "artifact_disk=${RAW_DISK}" \
    -var "ssh_private_key=${SSH_KEY}" \
    "${ROOT}/template-second-boot.pkr.hcl" \
    2>&1 | tee "${OUT}/validate2.log" \
    || fail "packer validate failed for template-second-boot.pkr.hcl (see ${OUT}/validate2.log)"

if [ -n "${VALIDATE_ONLY:-}" ]; then
    printf '\nE2E PERSIST RESULT: VALIDATE PASS (VALIDATE_ONLY=1; builds not run)\n'
    exit 0
fi

# ---------------------------------------------------------------------------
# 6. build 1 (persist mode)
# ---------------------------------------------------------------------------
printf '\n== [6/8] packer build 1 (persist mode) ==\n'
cd "${ROOT}" || exit 1
rm -rf output-persist output-second-boot
"${PACKER_BIN}" build "${BUILD1_ARGS[@]}" "${ROOT}/template.pkr.hcl" \
    | tee "${OUT}/build1.log"

RC=${PIPESTATUS[0]}
if [ "${RC}" -ne 0 ]; then
    printf '\nE2E PERSIST FAIL: build 1 exited %d (full log: %s)\n' "${RC}" "${OUT}/build1.log" >&2
    exit 1
fi

# The cloud-hypervisor builder copies the writable disk to
# output-<buildname>/ (builder.go; step_collect_artifact.go). Build 1's name
# is "persist", so the artifact is output-persist/<raw-basename>.
ARTIFACT_DISK="${ROOT}/output-persist/$(basename "${RAW_DISK}")"
if [ ! -f "${ARTIFACT_DISK}" ]; then
    fail "build 1 produced no artifact at ${ARTIFACT_DISK} (expected output-persist/)"
fi
printf 'build 1 artifact: %s\n' "${ARTIFACT_DISK}"

# ---------------------------------------------------------------------------
# 7. build 2 (chained second boot of the artifact)
# ---------------------------------------------------------------------------
printf '\n== [7/8] packer build 2 (chained second boot) ==\n'
BUILD2_ARGS=(
    -var "firmware=${FIRMWARE}"
    -var "artifact_disk=${ARTIFACT_DISK}"
    -var "ssh_private_key=${SSH_KEY}"
)

"${PACKER_BIN}" build "${BUILD2_ARGS[@]}" "${ROOT}/template-second-boot.pkr.hcl" \
    | tee "${OUT}/build2.log"

RC=${PIPESTATUS[0]}
if [ "${RC}" -ne 0 ]; then
    printf '\nE2E PERSIST FAIL: build 2 exited %d (full log: %s)\n' "${RC}" "${OUT}/build2.log" >&2
    exit 1
fi

# ---------------------------------------------------------------------------
# 8. post-build verification (the in-guest assertions ran inside each build)
# ---------------------------------------------------------------------------
printf '\n== [8/8] post-build verification ==\n'
[ -f "${ARTIFACT_DISK}" ] || fail "build 1 artifact missing after build 2: ${ARTIFACT_DISK}"
[ -d "${ROOT}/output-second-boot" ] || fail "build 2 output directory missing: output-second-boot/"
[ -d "${ROOT}/output-persist" ] || fail "build 1 output directory missing: output-persist/"

printf '\nE2E PERSIST RESULT: PASS (extensions merged during build 1 and re-merged at the chained second boot; see %s, %s)\n' \
    "${OUT}/build1.log" "${OUT}/build2.log"
