# Persist-mode E2E acceptance harness

This directory implements the persist-mode acceptance test: a build
that leaves extension images in `/var/lib/extensions` and `/var/lib/confexts`,
merges them during the build, enables the boot services, and re-applies the
extensions at a **second boot** of the built artifact.

Scope: the persist acceptance path, including the chained second boot. The
harness runs on the **cloud-hypervisor** builder (`packer-plugin-cloud-hypervisor`,
pinned ref — see below); the second boot is a **chained second Packer build**
that boots the build-1 artifact as its input disk. Bake-mode E2E lives in
`../bake/` and mirrors these conventions.

## Layout

| File | Role |
|---|---|
| `template.pkr.hcl` | Build 1: cloud-hypervisor firmware boot (EDK2) of the pinned Ubuntu 24.04 cloud image (systemd 255); `sysext` + `confext` persist provisioners (`merge_during_build=true`); in-build assertion shell provisioner (`assert.sh`); artifact is the modified raw disk copied to `output-persist/` |
| `template-second-boot.pkr.hcl` | Build 2: second cloud-hypervisor boot whose `disk_images.path` is `var.artifact_disk` (the build-1 artifact, passed via `-var`); no extension provisioners; a shell provisioner runs `assert-second-boot.sh` after the fresh boot |
| `run.sh` | Orchestrator: prereq aggregate, plugin build+install (both plugins), pinned assets + qcow2→raw conversion, cloud-init seed disk, TAP setup, build 1, artifact resolution, build 2, post-build verification |
| `assert.sh` | In-guest during-build assertions (uploaded by build 1's shell provisioner) |
| `assert-second-boot.sh` | In-guest post-boot assertions (uploaded by build 2's shell provisioner) |
| `fixtures/sysext-persist-hello/` | Directory source landing a known executable at `/usr/bin/sysext-persist-hello` |
| `fixtures/confext-persist-hello/` | Directory source landing a known file at `/etc/confext-persist-hello.conf` |
| `.out/` (generated, gitignored) | Assets (firmware, image, raw), seed disk, validate/build logs |

The second-boot assertions live in `assert-second-boot.sh` and run **inside
build 2** (via its shell provisioner), matching how build 1 runs `assert.sh`
and how the bake harness runs its assertions in-build. `run.sh` therefore only
verifies build exit codes and artifact/output presence after build 2.

## Required packages

- Linux host with `/dev/kvm` accessible (checked first; there is no TCG
  fallback with cloud-hypervisor)
- Cloud-Hypervisor binary >= v38 (`cloud-hypervisor` on `$PATH`, or
  `ch_binary_path`) — checked by the prereq aggregate
- Packer >= 1.9 — checked by the prereq aggregate
- Go >= 1.26 (`.go-version`) — builds the systemd-ext plugin and the pinned
  cloud-hypervisor plugin; also runs the prereq checker
  (`go run ./test/e2e/prereq/cmd/prereq`)
- `qemu-img` — required for the qcow2 → raw conversion (checked by the
  prereq aggregate; a hard prerequisite)
- `mtools` (`mformat` + `mcopy`) — builds the cloud-init NoCloud seed disk
- `curl`, `sha256sum`, `git` — pinned-asset download/verification and the
  cloud-hypervisor plugin checkout fallback
- `iproute2` (`ip`) + root/sudo — TAP device `ch-tap-0` creation
- `setcap` (libcap) + root/sudo — grants the cloud-hypervisor binary
  `cap_net_admin+ep` so it can attach the existing TAP device; only needed
  when the harness does not run as root
- `erofs-utils` (provides `mkfs.erofs`) — development prerequisite for the
  plugin's erofs packaging path; not used by this harness

## Pinned assets and plugin ref

| Item | Value |
|---|---|
| EDK2 firmware URL | `https://github.com/cloud-hypervisor/edk2/releases/download/ch-1e1b96f126/CLOUDHV.fd` |
| EDK2 sha256 | `9fb511fc0dd423d90a79615a90a8ace9b9e078b4a115ea2c459e0ac2f4e60218` |
| Ubuntu image URL | `https://cloud-images.ubuntu.com/releases/24.04/release/ubuntu-24.04-server-cloudimg-amd64.img` |
| Ubuntu image sha256 | `0533b0655c32e68b31d792ecd6ccfca95abdbc536c4446874fe0513bd4140ffe` |
| systemd | 255 (satisfies the >= 255 floor and the 252/254 provisioner floors) |
| cloud-hypervisor plugin ref | `979d702db885417820fa27fbcfce42256c90e110` (`CH_PLUGIN_REF`) |

`run.sh` downloads the assets into `.out/assets/` and verifies both sha256
hashes before use, then converts the cloud image to raw with
`qemu-img convert -O raw`. The cloud-hypervisor plugin is built from the
sibling checkout `~/workspace/packer-plugin-cloud-hypervisor` when present
(override with `CH_PLUGIN_SRC`); otherwise it is cloned from
`https://github.com/moeryomenko/packer-plugin-cloud-hypervisor.git` and
checked out at `CH_PLUGIN_REF`.

## Run

```sh
make e2e-persist                          # full chained build (see Makefile)
bash test/e2e/persist/run.sh              # equivalent
VALIDATE_ONLY=1 bash test/e2e/persist/run.sh # plugins+assets+seed+tap+validate only
SKIP_E2E=1 bash test/e2e/persist/run.sh   # explicit skip (see below)
```

Environment overrides (all optional):

| Env var | Default | Meaning |
|---|---|---|
| `E2E_IMAGE_URL` / `E2E_IMAGE_CHECKSUM` | pinned values | re-point or re-pin the base image |
| `CH_PLUGIN_REF` | pinned commit | cloud-hypervisor plugin ref to build |
| `CH_PLUGIN_SRC` | `~/workspace/packer-plugin-cloud-hypervisor` | sibling checkout to build from (else clone at `CH_PLUGIN_REF`) |
| `PACKER_BIN` | `packer` | packer binary |
| `SKIP_E2E` | unset | `1` = loud skip, no assertions |
| `VALIDATE_ONLY` | unset | `1` = validate only, no builds |

## Chained-build flow

1. **Prereq aggregate** — `/dev/kvm`, cloud-hypervisor >= 38, packer >= 1.9,
   qemu-img, go >= 1.26, TAP/root. Fails fast with an actionable message
   naming the missing requirement.
2. **Plugins** — the systemd-ext plugin under test plus the pinned
   cloud-hypervisor builder are installed into a hermetic
   `PACKER_PLUGIN_PATH` via `packer plugins install --path`.
3. **Assets** — EDK2 firmware + Ubuntu 24.04 image (pinned checksums),
   `qemu-img convert` to raw.
4. **Seed disk + TAP** — a cloud-init NoCloud seed (vfat, label `cidata`)
   is built with mtools and attached as a READ-ONLY second disk; `ch-tap-0`
   is created/configured via sudo (idempotent).
5. **Build 1** (`template.pkr.hcl`) — persists both extensions with
   `merge_during_build = true`, runs `assert.sh` in-guest.
6. **Artifact resolution** — the cloud-hypervisor builder copies the
   writable disk to `output-<buildname>/`; with build name `persist` the
   artifact is `output-persist/<raw-basename>`.
7. **Build 2** (`template-second-boot.pkr.hcl`) — boots that artifact fresh
   with `-var artifact_disk=...`, runs `assert-second-boot.sh` in-guest.
8. **Post-build verification** — host-side artifact/output checks; PASS only
   after both builds and all in-guest assertions succeed.

## Networking

The guest uses a static IP: the template's `network_interfaces.ip`
(`192.168.249.2`, a plain address without a `/24` suffix because the
cloud-hypervisor API deserializes `net[].ip` as `IpAddr`, paired with the
dotted-quad `mask` `255.255.255.0` which CH requires whenever an `ip` is set)
is what the builder's SSH host detection returns, so the cloud-init
`network-config` pins the guest to that address (matching the virtio-net
driver). The host TAP `ch-tap-0` carries `192.168.249.1/24` and is brought up
by `run.sh`. No NAT is required for direct host→guest SSH.

## Expected time and size bounds

- Download: ~600 MB (24.04 amd64 cloud image) + ~4 MB EDK2 firmware; both
  cached in `.out/assets/` and checksum-verified on every run
- Disk: the converted raw image is ~2.1 GB sparse; build 1's artifact is the
  same size; `output-persist/` + `output-second-boot/` hold one copy each
- Build 1 (firmware boot + cloud-init first boot + provisioning):
  ~5-15 min with KVM
- Build 2 (fresh boot of the artifact): ~3-10 min (`ssh_timeout` is 20m;
  the assertion script waits up to 5 min for the boot-time merge)
- Full `make e2e-persist`: ~15-35 min on a KVM host

## Skip policy (plan A2 — never silently false-green)

- `SKIP_E2E=1` is the only sanctioned skip; it prints a loud
  "NO ASSERTIONS EXECUTED" banner and exits 0. Setting it is an explicit,
  visible operator decision.
- Any other failure — missing tool, failed prereq, failed plugin build,
  checksum mismatch, failed validate, failed build, failed guest assertion,
  missing artifact — exits non-zero with a specific message.
- PASS is printed only after build 1, build 2, and all in-guest assertions
  succeed.

## Design notes

- **Why the cloud-init seed re-ids `ubuntu` to uid 0**: the plugin preflight
  runs `id -u` over the SSH communicator and fails with
  `NOT_ROOT` unless the session reports uid 0, and the packer SSH
  communicator has no sudo wrapper. SSH runs as `ubuntu`, so the
  seed's `bootcmd` re-ids the ubuntu user to uid 0 (`usermod -o -u 0 ubuntu`,
  or `useradd -o -u 0` on the first boot when cloud-init's cc_users has not
  created the user yet) before packer connects.
- **Why SSH uses an ephemeral publickey, not a password**: the stock Ubuntu
  24.04 cloud image rejects password logins on a fresh first boot even with a
  correctly hashed `ubuntu`/`ubuntu` password (verified via
  chroot `crypt(3)` MATCH plus live `sshpass` rejection — sshd/PAM reports
  "Failed password" regardless of the hash), while publickey auth succeeds.
  `run.sh` generates `.out/e2e-ssh-key` and the seed's `bootcmd` installs the
  public half into `/home/ubuntu/.ssh/authorized_keys` before sshd accepts
  connections. (The "proven firmware-boot example" assumption that password
  auth works unmodified does not hold against a stock image plus a
  root-only preflight.)
- **Why the seed pre-creates the install dirs**: a stock Ubuntu cloud image
  has neither `/var/lib/extensions` nor `/var/lib/confexts`, and the
  provisioner's atomic placement (`mv staging installDir/<name>.tmp`)
  fails with ENOENT when the destination parent is missing. The same
  `bootcmd` that re-ids `ubuntu` therefore also runs
  `mkdir -p /var/lib/extensions /var/lib/confexts` before packer connects
  (build 1 only; build 2 boots the artifact with the dirs baked in).
- **Why build 1 ends with a `sync` provisioner**: the pinned cloud-hypervisor
  plugin force-deletes the VM when the graceful shutdown exceeds its 30s
  timeout (step_shutdown_vm.go), and a force-killed guest loses its unflushed
  dirty pages. Without a final `sync` in the guest, the build-1 artifact was a
  stale copy missing the persisted extensions, the enabled-service symlinks
  and the SSH host keys, so build 2 could not boot/SSH it. `sync` puts every
  write on disk before the plugin starts the shutdown.
- **Why build 1 pre-enables the boot services before the provisioners**: the
  persist provisioners merge during the build then enable their boot service
  (`systemctl enable systemd-sysext.service` / `systemd-confext.service`).
  `systemd-sysext merge` overlays `/usr` read-only, so its enable (writing
  `/etc`) succeeds, but `systemd-confext merge` overlays `/etc` itself
  read-only and the plugin's enable would fail with `SERVICE_ENABLE_FAILED`.
  The template therefore runs a `shell` provisioner first that enables both
  units while `/etc` is writable; the provisioners' later enable is then a
  no-op (already enabled) and the `is-enabled` verification passes. The
  enabled symlinks are baked into the artifact for build 2.
- **Why a seed disk and not a CD-ROM**: the cloud-hypervisor plugin has no
  CD-ROM support, so the NoCloud seed is a second READ-ONLY raw disk (vfat,
  label `cidata`) attached to build 1 only. Build 2 boots the provisioned
  artifact without a seed: the guest network config (netplan), the ubuntu
  user, and the persisted extensions are baked into the artifact by build 1.
- **Merged-state assertion**: `systemd-sysext status` historically prints
  `STATUS: MERGED` (systemd >= 255) or `SYSEXTS ARE MERGED` (< 255) when
  merged; systemd 255.4 on Ubuntu 24.04 instead prints a table whose merged
  hierarchy row lists the extension (not `none`). The assert scripts match
  both the exact positive markers AND the table form (a row starting with a
  path whose EXTENSIONS column is not `none`), so `STATUS: NOT MERGED`
  (which contains the substring "MERGED") can never falsely pass (see
  `merged_state()`/`table_merged()` in both assert scripts).
  `assert-second-boot.sh` first waits up to 5 minutes for the boot-time
  merge, because packer connects as soon as SSH is up.
- **`extensions` uses block syntax** (`extensions { ... }` per entry),
  matching the generated `provisioner.hcl2spec.go` (`BlockListSpec`).
- **Pristine artifact**: build 2 boots the artifact directly; the harness
  does not modify it (no provisioners write to the guest), so the artifact
  remains a valid bootable image after the test.
- **confext noexec**: the confext fixture is data only — `/etc` is merged
  `nosuid,noexec` by systemd, so no confext payload is executed.
- **Artifact discovery**: the cloud-hypervisor builder copies writable disks
  to `output-<buildname>/` (`builder/cloud-hypervisor/step_collect_artifact.go`),
  so `run.sh` resolves the build-1 artifact from `output-persist/` without a
  manifest post-processor.
- **TAP IP semantics**: cloud-hypervisor only assigns `net.ip`/`mask` to TAP
  devices it creates itself; a pre-existing named TAP (created by run.sh) is
  left untouched, so the template's `ip` is purely the SSH host detection
  hint for the plugin's `CommHost`.

## CI

E2E is local-only (no CI runner; GitHub-hosted runners have no `/dev/kvm`).
The canonical gate is `make e2e` (bake then persist) or `make e2e-persist`
for this harness alone; `.github/workflows/e2e.yml` is a
`workflow_dispatch`-only scaffold for self-hosted `kvm`-labeled runners.
