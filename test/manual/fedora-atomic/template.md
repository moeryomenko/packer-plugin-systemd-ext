# Run notes — Fedora Atomic manual sanity

Copy this file per run, fill every field, and keep it with the ticket it
supports. Unfilled fields mean the run was not fully recorded.

## 1. Run identity

| Field | Value |
|-------|-------|
| Date (YYYY-MM-DD) | |
| Operator | |
| Ticket | |
| Plugin checkout (commit) | |

## 2. Target platform

| Field | Value |
|-------|-------|
| Distro | e.g. Fedora Silverblue / Fedora CoreOS / Fedora IoT |
| Distro version | e.g. 41 (include `VERSION_ID`, `VARIANT` from `/etc/os-release`) |
| systemd version | e.g. `systemd 256 (256.1-1.fc41)` — require major >= 254 |
| Rootfs type | e.g. ostree, read-only `/usr`, writable `/etc` overlay |
| Read-only proof | e.g. `findmnt -n -o FSTYPE,OPTIONS /usr` output |
| Execution model | Option A (dedicated KVM host) / Option B (atomic host as build host) |

## 3. Environment (build host)

| Field | Value |
|-------|-------|
| packer version | |
| go version | |
| qemu-system-x86_64 version | |
| qemu-img version | |
| cloud-localds / seed tool | |
| KVM check (`/dev/kvm` accessible) | OK / MISSING |

## 4. Extensions under test

| Name | Type (sysext/confext) | Source path | Format |
|------|-----------------------|-------------|--------|
| vc09-sysext-hello | sysext | `extensions/vc09-sysext-hello` | directory |
| vc09-confext-hello | confext | `extensions/vc09-confext-hello` | directory |

## 5. Persist-mode run

| Field | Value |
|-------|-------|
| Template path | |
| Command used | `packer build vc09-persist.pkr.hcl` |
| `merge_during_build` | true / false |
| Build artifact path | e.g. `output-atomic-guest/<disk-image>` |
| Reboot method | e.g. `systemctl reboot` in guest / fresh boot of artifact |
| Guest access after boot | e.g. `ssh fedora@<guest-ip>` |

### 5.1 During-build results

| Check | Expected | Actual |
|-------|----------|--------|
| `systemd-sysext status` | merged | |
| `systemd-confext status` | merged | |
| `systemctl is-enabled systemd-sysext.service` | enabled | |
| `systemctl is-enabled systemd-confext.service` | enabled | |
| `/usr/bin/vc09-sysext-hello` present + executable | yes | |
| `/etc/vc09-confext-hello.conf` present | yes | |
| image retained in `/var/lib/extensions` | yes | |
| image retained in `/var/lib/confexts` | yes | |

### 5.2 Post-reboot results (after fresh boot AND after reboot)

| Check | Expected | Actual |
|-------|----------|--------|
| `systemd-sysext status` (auto-merged after reboot) | merged | |
| `systemd-confext status` (auto-merged after reboot) | merged | |
| `systemctl is-enabled systemd-sysext.service` | enabled | |
| `systemctl is-enabled systemd-confext.service` | enabled | |
| `/usr/bin/vc09-sysext-hello` present + executable | yes | |
| `/etc/vc09-confext-hello.conf` present | yes | |
| image retained in `/var/lib/extensions` | yes | |
| image retained in `/var/lib/confexts` | yes | |

### 5.3 Persist outcome

- **Outcome**: PASS / FAIL / filed defect (defect link: )
- Observed output:
- Notes (divergences, retries, unexpected output):

## 6. Bake-mode run

| Field | Value |
|-------|-------|
| Template path | |
| Command used | `packer build vc09-bake.pkr.hcl` |
| Build artifact path (if any) | |

Record **each provisioner separately** (on ostree, `/usr` is read-only but
`/etc` is a writable overlay, so sysext and confext outcomes can differ).

### 6.1 `systemd-ext-sysext` (bake)

- Outcome: SUCCESS / ACTIONABLE-ERROR / FAIL
- If SUCCESS: payload at `/usr/bin/vc09-sysext-hello`, `/var/lib/extensions`
  empty, baked release file absent?
- If ACTIONABLE-ERROR (expected on a read-only rootfs): paste the error text
  verbatim. Confirm it (a) names the failing operation, (b) contains
  `Read-only file system`, (c) carries `[BAKE_FAILED]`/`[MERGE_FAILED]`:

### 6.2 `systemd-ext-confext` (bake)

- Outcome: SUCCESS / ACTIONABLE-ERROR / FAIL
- If SUCCESS: payload at `/etc/vc09-confext-hello.conf`, `/var/lib/confexts`
  empty, baked release file absent?
- If ACTIONABLE-ERROR: paste the error text verbatim; confirm (a)-(c) above:

### 6.3 Bake outcome

- **Outcome**: SUCCESS / ACTIONABLE-ERROR / FAIL
- Any unactionable failure (panic, stack trace, hang, SSH failure) is a defect:
  defect link:

## 7. Overall result

| Field | Value |
|-------|-------|
| Persist-mode result | PASS / FAIL / PENDING-DEFECT |
| Bake-mode result | SUCCESS / ACTIONABLE-ERROR / FAIL |
| Notes completeness | complete / incomplete (missing fields: ) |
| Overall result | PASS (persist PASS, bake success-or-actionable-error, notes complete) / FAIL (reason) |
