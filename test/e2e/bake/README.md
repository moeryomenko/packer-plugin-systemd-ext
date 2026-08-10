# E2E Bake Acceptance

End-to-end acceptance harness for **bake mode**, migrated to the
**cloud-hypervisor builder**. A real cloud-hypervisor guest is booted via UEFI
firmware (EDK2 `CLOUDHV.fd`) from a pinned systemd >= 255 Ubuntu 24.04 image,
the `sysext` and `confext` provisioners run in bake mode, and guest-side
assertions verify the conditions listed below.

## What it verifies

| Check |
|---|
| sysext directory-source file at merged path `/usr/bin/sysext-hello` (+ executable, + content) |
| sysext `.raw` source file at merged path `/usr/bin/sysext-raw-hello` (+ content) |
| confext directory-source file at merged path `/etc/e2e-conf/main.conf` (+ content) |
| `/var/lib/extensions` and `/var/lib/confexts` exist and are **empty** |
| baked `extension-release.d` files absent (3 paths) |
| `packer build` accepts the `sysext`/`confext` blocks (plugin installed via `--path`) |
| boot-service enablement state reported (non-fatal; see Notes) |

## Files

| File | Purpose |
|---|---|
| `template.pkr.hcl` | cloud-hypervisor builder (firmware boot) + `sysext`/`confext` bake provisioners + `shell` assert provisioner |
| `run.sh` | Full pipeline: prereq aggregate, build/install both plugins, fetch+verify pinned assets, TAP setup, fixtures + cloud-init seed, validate, build, report PASS/FAIL, cleanup |
| `assert.sh` | Guest-side assertions (uploaded by the shell provisioner) |
| `fixtures/sysext-src/bin/sysext-hello` | sysext **directory** source (wrapped as `usr/...` by the plugin) |
| `fixtures/confext-src/e2e-conf/main.conf` | confext **directory** source (wrapped as `etc/...` by the plugin) |

The prebuilt `.raw` fixture (`sysext-raw`) is **not** committed: `run.sh`
generates it with `mksquashfs` (layout below) so no binary artifacts live in
the tree and the tool requirement is explicit.

## Prerequisites (host)

| Tool | Provides | Notes |
|---|---|---|
| `/dev/kvm` | VM acceleration | cloud-hypervisor requires an accessible KVM device |
| `cloud-hypervisor` (>= v38.0) | guest boot | on `$PATH` or `ch_binary_path`; `run.sh` prereq checks `--version` >= 38 |
| `packer` (>= 1.9) | build/validate | workflow installs a pinned 1.11.2 |
| `qemu-img` | qcow2 -> raw conversion | hard prerequisite |
| `go` (>= 1.26) + `make` | plugin builds | builds the systemd-ext plugin and the pinned cloud-hypervisor plugin from source |
| TAP + root/sudo | guest networking | `sudo ip tuntap add dev ch-tap-0 mode tap` |
| `setcap` (libcap) + root/sudo | cloud-hypervisor TAP attach | grants the cloud-hypervisor binary `cap_net_admin+ep` so it can attach the existing TAP device; only needed when the harness does not run as root |
| `mksquashfs` + `unsquashfs` | raw fixture | package `squashfs-tools` |
| `ar` + `tar --zstd` | guest `systemd-dissect` extraction | from the pinned `systemd-container` .deb (see below) |
| `cloud-localds` / `genisoimage` / `xorriso` | cloud-init seed ISO | any one; `xorriso` ships on most distros |
| `curl` + `sha256sum` | pinned asset fetch | standard |
| `erofs-utils` | `mkfs.erofs` | development prerequisite for the erofs round-trip unit tests (`internal/extpkg`); not used by this harness |

`run.sh` runs the fail-fast aggregate (`go run ./test/e2e/prereq/cmd/prereq`)
first and stops with an actionable message naming the missing item.

## Pinned assets

`run.sh` downloads and verifies (hard sha256 pin; mismatch fails loudly):

| Asset | Value |
|---|---|
| EDK2 firmware | `https://github.com/cloud-hypervisor/edk2/releases/download/ch-1e1b96f126/CLOUDHV.fd` |
| Firmware sha256 | `9fb511fc0dd423d90a79615a90a8ace9b9e078b4a115ea2c459e0ac2f4e60218` |
| Ubuntu 24.04 image | `https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img` |
| Image sha256 | `0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe` (verified against `.../release/SHA256SUMS` 2026-08-05) |
| systemd-container .deb | `https://archive.ubuntu.com/ubuntu/pool/main/s/systemd/systemd-container_255.4-1ubuntu8.16_amd64.deb` (provides the guest `systemd-dissect` binary; see Notes) |
| .deb sha256 | `7ab146ac98bf9c6095935b37a906d589795105c301626f2fd5db6d0e77bd9472` (verified 2026-08-06) |
| systemd | 255 (satisfies the >= 255 floor and the 252/254 provisioner floors) |

The Ubuntu pin is cross-checked against the live `SHA256SUMS` on every run
(the "SHA256SUMS fetch pattern"): if the published hash drifts from the pin,
`run.sh` prints a loud warning so a re-pin is deliberate. Override pins with
`E2E_FIRMWARE_URL` / `E2E_FIRMWARE_SHA256` / `E2E_IMAGE_URL` /
`E2E_IMAGE_SHA256`.

The cloud-hypervisor builder plugin is pinned to commit
`979d702db885417820fa27fbcfce42256c90e110` (`CH_PLUGIN_REF`); `run.sh` builds
from the sibling checkout at `/home/eryoma/workspace/packer-plugin-cloud-hypervisor`
when present at that SHA, otherwise clones
`https://github.com/moeryomenko/packer-plugin-cloud-hypervisor.git` and checks
out the pin. Bump `CH_PLUGIN_REF` deliberately and re-run `make e2e-bake`.

**If you change the image**, also update the raw-fixture release values
(`E2E_RAW_ID` / `E2E_RAW_VERSION`, defaults `ubuntu` / `24.04`): the `.raw`
source's in-image `extension-release.sysext-raw` must match
the guest's os-release or the in-guest merge rejects it.

## Networking

The cloud-hypervisor plugin derives the SSH target from the network
interface's `ip` field (builder `CommHost`), so the guest is given the static
address `10.0.2.2/24` via the cloud-init NoCloud seed ISO that `run.sh`
generates. The host side of TAP `ch-tap-0` carries the gateway `10.0.2.1/24`.
The interface `ip` is the plain address `10.0.2.2` (no `/24` suffix) with the
dotted-quad `mask` `255.255.255.0`: the cloud-hypervisor API deserializes
`net[].ip` as `IpAddr` (rejecting CIDR syntax) and requires a `mask` whenever
an `ip` is set. For a TAP that already carries an address cloud-hypervisor
preserves the existing host-side IP, so the interface `ip` here is
effectively used as the SSH host only. SSH authenticates with an ephemeral
publickey (`run.sh` generates `.out/e2e-ssh-key` and the seed's `bootcmd`
installs the public half into `/home/ubuntu/.ssh/authorized_keys`): password
auth is not usable on a fresh first boot of this image (sshd/PAM rejects even
a correctly hashed `ubuntu`/`ubuntu` password — verified via chroot
`crypt(3)` MATCH plus live `sshpass` rejection — while publickey auth
succeeds). The same `bootcmd` re-ids `ubuntu` to uid 0 —
`usermod -o -u 0` / `useradd -o -u 0` on first boot — to satisfy the
provisioner's root
preflight, since the packer SSH communicator has no sudo wrapper),
with `ssh_timeout = "20m"` for the slow UEFI + cloud-init first boot.

## Run

```sh
test/e2e/bake/run.sh            # full build (downloads ~600MB image, boots the VM)
VALIDATE_ONLY=1 test/e2e/bake/run.sh   # prereq+plugins+assets+TAP+fixtures+seed+validate only
SKIP_E2E=1 test/e2e/bake/run.sh        # explicit skip (see below)
make e2e-bake                   # canonical target from the repo root
```

Expected run time: **10-20 min** on a KVM-capable host (mostly asset download
+ cloud-init first boot). Logs: `.out/build.log`, `.out/validate.log`.

## Skipping vs false-green

The suite is **skippable but never silently false-green**:

- `SKIP_E2E=1` is the only sanctioned skip; it prints a loud
  "NO ASSERTIONS EXECUTED" banner and exits 0. Setting it is an explicit,
  visible operator decision.
- Any other failure — missing tool, failed prereq, failed build, failed
  validate, failed guest assertion, checksum mismatch — exits non-zero with a
  specific message.
- After a successful `packer build`, `run.sh` additionally requires the
  `RESULT: ALL E2E BAKE ASSERTIONS PASSED` marker in `build.log`; a build
  whose guest assertions silently did not run can never report green.

## Notes and design decisions

- **`extensions` uses block syntax** (`extensions { ... }` per entry), matching
  the generated `provisioner.hcl2spec.go` (`BlockListSpec`).
- **Provisioner naming**: Packer namespaces every literal component
  of a plugin as `<pluginName>-<component>` (binary
  `packer-plugin-systemd-ext` -> `systemd-ext-`). The working spellings are
  `provisioner "systemd-ext-sysext"` and `provisioner "systemd-ext-confext"`;
  see the spec/README reconciliation notes.
- **Seed ISO**: `run.sh` writes `meta-data`/`user-data`/`network-config` under
  `.out/seed/` and packs them into an ISO9660 volume labeled `cidata` (NoCloud)
  with `cloud-localds`, `genisoimage`, or `xorriso`. The template attaches it
  as a readonly `disk_images` entry; the boot disk is the only writable disk
  and the only artifact captured. The seed's `bootcmd` re-ids `ubuntu` to
  uid 0 (the preflight) and pre-creates `/var/lib/extensions` and
  `/var/lib/confexts`: a stock Ubuntu cloud image has neither, and the
  provisioner's atomic placement (`mv` into the install dir) fails
  with ENOENT when the destination parent is missing.
- **Raw fixture sanity** uses `unsquashfs -l` (available wherever `mksquashfs`
  is). The authoritative content check is the in-guest
  `systemd-dissect --copy-from` during the bake.
- **Guest `systemd-dissect`**: the base Ubuntu 24.04 cloud image ships the
  binary only as a bash-completion stub (it lives in the `systemd-container`
  package, not base `systemd`), but the prebuilt-.raw bake requires
  `systemd-dissect --copy-from` in the guest for the prebuilt `.raw` fixture.
  `run.sh` extracts `/usr/bin/systemd-dissect` from the pinned
  `systemd-container_255.4-1ubuntu8.16_amd64.deb` (same checksum-pinned fetch
  pattern as the image/firmware) and the template's `file` provisioner uploads
  it to `/usr/bin/systemd-dissect` before any extension provisioner runs.
  Re-pin the .deb deliberately when the pinned Ubuntu image's systemd version
  changes.
- **Service enablement is reported, not asserted.** The acceptance run does not
  require it, and Ubuntu 24.04 ships `systemd-sysext.service`/`systemd-confext.service`
  enabled by default, so `is-enabled` output cannot distinguish "plugin enabled
  it" from "image preset". `assert.sh` prints the state as evidence.
- **TAP cleanup**: `run.sh` deletes `ch-tap-0` on exit only when it created
  the device; a pre-existing TAP is reused and left untouched.
- **Per-run unique instance-id**: `run.sh` writes a fresh
  `INSTANCE_ID="packer-bake-e2e-$(date +%s)"` into the NoCloud seed's
  `meta-data` on every run. A constant instance-id makes cloud-init treat a
  reused writable raw disk as the same instance (`new=False`) and never re-apply
  the seed network-config, so the guest never configures `10.0.2.2` and the
  packer SSH communicator hangs at "Waiting for SSH to become available...".
  The unique id forces a new instance, so the seed network config is applied on
  every run; the raw disk conversion cache stays valid across runs.
- Generated state under `.out/` (raw, seed, ssh-free fixtures, logs) and
  `output-*/` / `packer_cache/` build outputs are gitignored — inspect them
  for debugging.
