# Manual sanity procedure — Fedora Atomic

This procedure is a manual sanity check of the `systemd-ext` plugin's two
provisioner modes on a Fedora Atomic guest. It covers:

- **Persist mode**: install a sysext and a confext on a guest whose rootfs is
  read-only, with `merge_during_build = true`, then verify the extensions stay
  merged across a fresh boot and a reboot of the guest.
- **Bake mode**: bake the same extensions into the rootfs, expecting either
  success (when `/usr` and `/etc` are writable) or a clearly actionable error
  (when the rootfs is read-only).

Only these two modes and this distro family are exercised here. Executing the
procedure produces run notes (see [`template.md`](template.md)); the filled
notes are the record of the run.

---

## 1. Target platform

**Fedora Atomic distribution** means an rpm-ostree based Fedora variant —
Fedora Silverblue (workstation), Fedora CoreOS (server), or Fedora IoT — where:

- `/usr` is an ostree deployment, **mounted read-only**;
- `/etc` is a writable overlay merged over the immutable `/usr/etc`;
- `/var` is writable (so `/var/lib/extensions` and `/var/lib/confexts` work).

The target guest MUST satisfy:

| Requirement | Floor | Check |
|-------------|-------|-------|
| systemd major version | `>= 254` (confext floor; sysext floor is 252) | `systemctl --version \| head -1` |
| `/usr` read-only rootfs | yes | `findmnt -n -o FSTYPE,OPTIONS /usr` shows `ro`; `touch /usr/.vc09-probe` fails |

Reference mapping (Fedora releases with systemd >= 254): Fedora 39 -> 254,
Fedora 40 -> 255, Fedora 41+ -> 256+. Fedora < 39 does NOT satisfy the floor.

### 1.1 Execution model: where the run happens

Packer provisioners execute **inside a guest over SSH**; the plugin's preflight
requires the SSH user to be uid 0. The guest is always a Fedora Atomic VM. Two
equivalent setups are supported:

- **Option A (recommended) — dedicated KVM build host**: a Linux host with
  `/dev/kvm` boots a Fedora Silverblue/CoreOS VM via the qemu builder, runs the
  provisioners in the guest, then reboots the guest for the persistence check.
- **Option B — the Fedora Atomic host itself is the build host**: the same
  procedure run from a shell on a Silverblue/CoreOS machine that has KVM
  (`/dev/kvm`) and installs a test VM of the same distro family. The atomic host
  is NOT itself the packer target; a VM guest is, because packer has no local
  builder for these provisioners.

Choose the setup before starting and record it in the run notes.

### 1.2 Prerequisites (fail-fast)

Run each check; stop and fix if any fails.

#### 1.2.1 Build host (Option A host, or the atomic host in Option B)

```sh
test -r /dev/kvm && echo "KVM: OK" || echo "KVM: MISSING /dev/kvm"
packer --version                       # require >= 1.9.0
go version                             # require >= 1.25 (repo floor; go.mod toolchain)
qemu-img --version | head -1           # required to inspect/convert images if needed
cloud-localds --version || echo "cloud-localds missing (install cloud-image-utils)"
qemu-system-x86_64 --version | head -1 # qemu builder backend
```

`cloud-localds` builds the cloud-init NoCloud seed ISO used in Section 2.3. If
it is unavailable, `genisoimage`/`mkisofs` can build the same ISO.

#### 1.2.2 Target guest (the Fedora Atomic VM)

Verify these ON the guest after it boots, before any packer run:

```sh
cat /etc/os-release          # expect ID=fedora, VARIANT="Silverblue"/"CoreOS", OSTREE_VERSION
rpm-ostree status            # expect an ostree deployment (confirms Atomic/ostree)
systemctl --version | head -1   # expect "systemd 254 (254.x)" or higher
findmnt -n -o FSTYPE,OPTIONS /usr   # expect "... ro, ..." (read-only /usr)
touch /usr/.vc09-write-probe; echo "unexpected: /usr writable"; \
  [ -e /usr/.vc09-write-probe ] && echo "unexpected: /usr writable" || echo "OK: /usr is read-only"
```

If `systemctl --version` reports a major < 254, the guest does not satisfy the
floor: record FAIL in the run notes and stop (the plugin preflight would fail
fast with `SYSTEMD_TOO_OLD`).

---

## 2. Persist-mode run

### 2.1 Install the plugin on the build host

From the repository checkout on the build host:

```sh
make build
packer plugins install --path ./packer-plugin-systemd-ext systemd-ext
```

Verify the provisioners are registered:

```sh
packer build --help | grep -i systemd-ext   # or: packer plugins installed | grep systemd-ext
```

Expect the accessors `systemd-ext-sysext` and `systemd-ext-confext` (the plugin
binary is `packer-plugin-systemd-ext`, so components are namespaced
`<plugin>-<component>`).

### 2.2 Prepare the extension directories (fixtures)

Directory sources only (default `format = "directory"`; no `mksquashfs` or
`mkfs.erofs` needed). The plugin auto-generates the `extension-release` file
from the guest's `/etc/os-release`, so no manual release file is required.
Prebuilt `.raw` sources are intentionally out of scope here because they must
carry a guest-matching release file.

```sh
mkdir -p extensions/vc09-sysext-hello/usr/bin
cat > extensions/vc09-sysext-hello/usr/bin/vc09-sysext-hello <<'EOF'
#!/bin/sh
echo "hello from vc09 sysext"
EOF
chmod +x extensions/vc09-sysext-hello/usr/bin/vc09-sysext-hello

mkdir -p extensions/vc09-confext-hello/etc
cat > extensions/vc09-confext-hello/etc/vc09-confext-hello.conf <<'EOF'
# vc09 confext marker
vc09-confext-hello = present
EOF
```

Layout note: the sysext tree is wrapped under `usr/` so its payload lands at
`/usr/bin/vc09-sysext-hello`; the confext tree is wrapped under `etc/` so its
payload lands at `/etc/vc09-confext-hello.conf`.

### 2.3 Prepare the guest boot (Fedora Atomic VM + cloud-init seed)

Download a Fedora Silverblue (or CoreOS) cloud image matching the target, e.g.:

```sh
curl -fLO https://download.fedoraproject.org/pub/fedora/linux/releases/41/Silverblue/x86_64/images/Fedora-Silverblue-ostree-x86_64-41-1.4.qcow2
```

Create a NoCloud seed that (a) lets packer log in as `fedora` and (b) re-ids
`fedora` to uid 0 so the plugin preflight (root required) passes — same
approach as the e2e harnesses:

```sh
cat > user-data <<'EOF'
#cloud-config
users:
  - default
password: vc09pass
chpasswd: { expire: False }
ssh_pwauth: true
runcmd:
  - [ usermod, -o, -u, "0", fedora ]
EOF
cat > meta-data <<'EOF'
instance-id: fedora-atomic-manual
local-hostname: fedora-atomic
EOF
cloud-localds seed.iso user-data meta-data
```

### 2.4 Persist-mode template

`vc09-persist.pkr.hcl` (both provisioners, `mode = "persist"`,
`merge_during_build = true`):

```hcl
variable "silverblue_qcow2" {
  type    = string
  default = "./Fedora-Silverblue-ostree-x86_64-41-1.4.qcow2"
}
variable "seed_iso" {
  type    = string
  default = "./seed.iso"
}

source "qemu" "atomic-guest" {
  iso_url      = var.silverblue_qcow2
  iso_checksum = "none"
  disk_image   = true          # boot the qcow2 as-is; no OS install
  accelerator  = "kvm"
  headless     = true
  memory       = 4096
  cpus         = 2
  disk_interface = "virtio"
  net_device     = "virtio-net"
  ssh_username   = "fedora"
  ssh_password   = "vc09pass"
  ssh_timeout    = "30m"       # first boot of a Silverblue cloud image is slow
  qemuargs       = [["-cdrom", var.seed_iso]]
}

build {
  sources = ["source.qemu.atomic-guest"]

  provisioner "systemd-ext-sysext" {
    mode               = "persist"
    merge_during_build = true
    extensions {
      name   = "vc09-sysext-hello"
      source = "${path.root}/extensions/vc09-sysext-hello"
    }
  }

  provisioner "systemd-ext-confext" {
    mode               = "persist"
    merge_during_build = true
    extensions {
      name   = "vc09-confext-hello"
      source = "${path.root}/extensions/vc09-confext-hello"
    }
  }

  # During-build assertions (mirror test/e2e/persist/assert.sh):
  #   status merged, boot units enabled, payloads visible, images retained.
  provisioner "shell" {
    inline = [
      "systemd-sysext status | grep -Ei 'STATUS: MERGED|SYSEXTS ARE MERGED'",
      "systemd-confext status | grep -Ei 'STATUS: MERGED|SYSEXTS ARE MERGED'",
      "test \"$(systemctl is-enabled systemd-sysext.service)\" = enabled",
      "test \"$(systemctl is-enabled systemd-confext.service)\" = enabled",
      "test -x /usr/bin/vc09-sysext-hello",
      "test -f /etc/vc09-confext-hello.conf",
      "test -d /var/lib/extensions/vc09-sysext-hello",
      "test -d /var/lib/confexts/vc09-confext-hello",
      "echo 'vc09 persist during-build assertions: PASS'"
    ]
  }
}
```

Run it:

```sh
packer build vc09-persist.pkr.hcl
```

**Expected during-build results (both provisioners):**
- `systemd-sysext status` / `systemd-confext status` report **merged**;
- `systemctl is-enabled systemd-sysext.service` and
  `systemctl is-enabled systemd-confext.service` print **enabled**;
- the payloads are visible at their merged paths and the images REMAIN in
  `/var/lib/extensions` and `/var/lib/confexts` (persist never removes them).

The build output artifact is the modified disk under
`output-atomic-guest/` (packer qemu builder) — record the path in the run
notes.

### 2.5 Reboot step (mandatory for the persistence proof)

Boot the persist artifact from Section 2.4 as a **fresh boot**, then reboot the
running guest once more. Both actions are required proof that the extensions
auto-merge after boot.

1. Boot the artifact (fresh boot of the persist image):

   ```sh
   virsh net-start default 2>/dev/null || true
   virt-install --connect qemu:///system \
     --name vc09-persist \
     --import --disk path=output-atomic-guest/<disk-image>,format=qcow2,bus=virtio \
     --memory 4096 --vcpus 2 --os-variant fedora-unknown \
     --network network=default --graphics none --noautoconsole
   ```

   (or boot it with `qemu-system-x86_64 -drive
   file=output-atomic-guest/<disk-image>,if=virtio,format=qcow2 -m 4096 -netdev
   user,id=n0,hostfwd=tcp::2222-:22 -device
   virtio-net-pci,netdev=n0 -nographic` and SSH to `127.0.0.1:2222`.)

2. Wait for boot, then SSH in (the `fedora` user is uid 0 and the password was
   set in 2.3):

   ```sh
   ssh fedora@<guest-ip> 'sudo true'   # or ssh -p 2222 fedora@127.0.0.1
   ```

3. Run the post-fresh-boot verification (Section 2.6) and record it.

4. Reboot the running guest:

   ```sh
   ssh fedora@<guest-ip> 'sudo systemctl reboot'
   # wait for the guest to come back up (ping / ssh retry loop)
   ```

5. Run the post-reboot verification again (Section 2.6) and record it.

### 2.6 Post-reboot verification (extension auto-merged after reboot)

```sh
systemd-sysext status | grep -Ei 'STATUS: MERGED|SYSEXTS ARE MERGED'   # expect merged
systemd-confext status | grep -Ei 'STATUS: MERGED|SYSEXTS ARE MERGED'  # expect merged
systemctl is-enabled systemd-sysext.service    # expect enabled
systemctl is-enabled systemd-confext.service   # expect enabled
test -x /usr/bin/vc09-sysext-hello && echo "sysext payload present"
test -f /etc/vc09-confext-hello.conf && echo "confext payload present"
test -d /var/lib/extensions/vc09-sysext-hello && echo "sysext image retained"
test -d /var/lib/confexts/vc09-confext-hello && echo "confext image retained"
```

**Expected: PASS** — all checks pass after the fresh boot AND after the
reboot; the extensions were re-merged by the enabled boot services with no
manual intervention. Record PASS (or a filed defect, with reproduction
details) in the run notes.

---

## 3. Bake-mode run

### 3.1 Bake-mode template

`vc09-bake.pkr.hcl`: identical to `vc09-persist.pkr.hcl` except the two
provisioner blocks use `mode = "bake"` and the during-build assertions are
replaced by a simple probe:

```hcl
  provisioner "systemd-ext-sysext" {
    mode = "bake"
    extensions {
      name   = "vc09-sysext-hello"
      source = "${path.root}/extensions/vc09-sysext-hello"
    }
  }

  provisioner "systemd-ext-confext" {
    mode = "bake"
    extensions {
      name   = "vc09-confext-hello"
      source = "${path.root}/extensions/vc09-confext-hello"
    }
  }
```

Run it on a **fresh** guest (revert to the untouched cloud image or use a new
VM name), because bake mutates the rootfs and removes the extension artifacts:

```sh
packer build vc09-bake.pkr.hcl
```

### 3.2 Expected outcomes

- **Success** — if the guest's `/usr` and `/etc` are writable: the extension
  payloads are copied into the rootfs, `/var/lib/extensions` and
  `/var/lib/confexts` end up empty (bake removes the artifacts), and the baked
  `extension-release.d` files are absent. Verify with:

  ```sh
  test -x /usr/bin/vc09-sysext-hello && echo "sysext baked into /usr"
  test -f /etc/vc09-confext-hello.conf && echo "confext baked into /etc"
  ls /var/lib/extensions /var/lib/confexts        # expect empty
  ls /usr/lib/extension-release.d /etc/extension-release.d 2>/dev/null || true
  ```

- **Actionable error** — on an ostree rootfs, the sysext bake copy step writes
  into read-only `/usr` and fails with the signature below. This is the
  EXPECTED outcome on Fedora Atomic; it is a PASS **provided** the error is
  actionable as defined in Section 3.3.

**Ostree asymmetry (record per provisioner, do not collapse):** on Fedora
Silverblue/CoreOS, `/usr` is read-only but `/etc` is a writable overlay. So the
`systemd-ext-sysext` bake run is expected to fail with an actionable error,
while the `systemd-ext-confext` bake run may succeed (writes into `/etc`).
Record each provisioner's outcome separately in the run notes.

### 3.3 Definition of actionable error (bake on a read-only rootfs)

A bake-mode failure is **actionable** when ALL of the following hold:

1. **It names the failing operation** — the error identifies the copy-into-
   rootfs step, e.g. `cp -a /var/lib/extensions/<name>/. /` (directory
   source) or `systemd-dissect --copy-from <image> / <tmp>/` (prebuilt `.raw`),
   or the release-file removal `rm -f .../extension-release.d/...`.
2. **It identifies the read-only rootfs as the cause** — the error detail
   contains the kernel/glibc message `Read-only file system` (the literal text
   `cp` prints on `EROFS`).
3. **It carries the plugin's machine-readable code** — the message ends with
   the stable code `[BAKE_FAILED]` (a failed bake guest command) or
   `[MERGE_FAILED]` (a failed validation merge).

Expected actionable error text on Fedora Atomic (sysext bake, directory
source; exact `cp` wording depends on the guest):

```
cp -a /var/lib/extensions/vc09-sysext-hello/. /: exit status 1: cp: cannot
create regular file '/usr/bin/vc09-sysext-hello': Read-only file system
[BAKE_FAILED]
```

A failure is **NOT actionable** (record as FAIL / defect) if it is:
- a Go panic or stack trace, a segfault/hang, or a build abort unrelated to
  rootfs writability;
- an SSH/communicator failure (the guest never ran the provisioner);
- an error that does NOT name the read-only rootfs, e.g. a release-file
  mismatch (`extension-release` ID/VERSION_ID mismatch is a different defect).

In all cases copy the exact failure text verbatim into the run notes.

---

## 4. Outcome

### 4.1 Recording the results

Fill in [`template.md`](template.md) for the persist run and the bake run.
Outcome vocabulary:

- **PASS** — behavior matched the expectation (persist merged + enabled across
  reboot; bake succeeded on a writable rootfs).
- **ACTIONABLE-ERROR** — bake failed with an actionable error per Section 3.3
  (expected on a read-only rootfs).
- **FAIL** — persist did not pass, bake crashed/panicked, or the error was not
  actionable. File a defect with the reproduction steps (commands, template,
  guest distro/version) and attach the run notes.

### 4.2 Overall result

Summarize the whole run with a single result and a reason:

- **PASS** — persist-mode notes show PASS (or a filed defect with reproduction
  details), the bake-mode notes show either success or an actionable error, and
  the run notes match the template.
- **FAIL** — any other combination; write the reason next to the result.

### 4.3 Cleanup (best-effort; keep the atomic host clean)

```sh
# on the guest:
systemctl disable systemd-sysext.service systemd-confext.service 2>/dev/null || true
systemd-sysext unmerge
systemd-confext unmerge
rm -rf /var/lib/extensions/vc09-sysext-hello /var/lib/confexts/vc09-confext-hello

# on the build host:
virsh destroy vc09-persist 2>/dev/null; virsh undefine vc09-persist 2>/dev/null
rm -f seed.iso user-data meta-data
rm -rf output-atomic-guest
```

### 4.4 What a passing run looks like

A run is passing when the filled notes show:

- the target guest satisfied the platform floor (distro + systemd >= 254 +
  read-only `/usr`), with the os-release/systemd/findmnt output recorded;
- persist mode PASSED during build and after both the fresh boot and the
  reboot (status merged, services enabled, payloads visible, images retained);
- bake mode either succeeded (payloads in the rootfs, artifacts removed) or
  failed with an actionable error per Section 3.3, with the actual output
  recorded per provisioner;
- every field of the template is filled.
