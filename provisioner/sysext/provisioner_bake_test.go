// Test contract: upload/atomic placement and the bake flow for the sysext
// provisioner, plus the bake portion of the e2e contract. The engineer
// implements Provision's bake path to match these tests exactly. Expected red
// state against the current stub: Provision returns "sysext provisioner: not
// implemented", so every test fails on behavior (no compile failure — the
// package layout is final).
//
// ---------------------------------------------------------------------------
// Pinned command-stream contract (the engineer implements to match)
// ---------------------------------------------------------------------------
//
// For one directory-format extension "extone" in bake mode, the exact
// ordered Start stream is wantSysextBakeDirectoryStream() below:
//
//	1. command -v systemd-sysext            preflight
//	2. systemctl --version                  preflight
//	3. id -u                                preflight
//	4. mkdir -p /var/tmp/packer-sysext-RANDOM  staging root
//	5. mv  <staging>/extone <install>/extone.tmpRANDOM   atomic placement
//	6. mv  <install>/extone.tmpRANDOM <install>/extone  (plain mv: overwrites)
//	7. chown -R root:root <install>/extone  ownership
//	8. systemd-sysext status                unmerge-if-merged check
//	9. systemd-sysext merge                 (validation)
//	10. systemd-sysext unmerge
//	11. cp -a <install>/extone/. /          directory copy
//	12. rm -rf <install>/extone             artifact removal
//	13. rm -f /usr/lib/extension-release.d/extension-release.extone
//
// Conventions pinned by these tests (documented so the bake implementation
// matches and the persist tests can reuse the canonical stream for
// idempotency accounting):
//
//   - Upload staging root: /var/tmp/packer-sysext-<random>/.
//     Directory artifacts upload via comm.UploadDir to
//     /var/tmp/packer-sysext-<random>/<name>; .raw artifacts and verity
//     companions upload via comm.Upload to
//     /var/tmp/packer-sysext-<random>/<basename> (raw first, then .verity,
//     .roothash, .roothash.p7s — the DetectVeritySet order).
//   - Atomic placement: per artifact basename b,
//     `mv <staging>/<b> <installDir>/<b>.tmp<random>` then
//     `mv <installDir>/<b>.tmp<random> <installDir>/<b>`. The final mv is a
//     plain `mv` (POSIX mv overwrites an existing destination; no -i/-n, no
//     pre-check — that is the overwrite semantics pinned by
//     TestBakeAtomicRenameOverwritesExistingFinal).
//   - Ownership: directory artifacts get
//     `chown -R root:root <installDir>/<name>`; file artifacts get
//     `chown root:root <installDir>/<b>`, after the final mv.
//   - The guest /etc/os-release is read once per run via
//     comm.Download("/etc/os-release"), before upload, only when at least
//     one directory-format source needs release generation. A
//     .raw-only run performs no Download. It is NOT a Start command; a
//     `cat /etc/os-release` implementation would break the exact-stream
//     assertion and must be raised with @build before changing this test.
//   - The guest staging root /var/tmp/packer-sysext-<random>/ is
//     created on the guest with `mkdir -p` before the first upload: the
//     scp-backed UploadDir/Upload calls fail with "No such file or directory"
//     when it is absent (real-QEMU defect). The mkdir
//     is a Start command pinned as step 4; it runs once per UploadAndPlace
//     call, before the first artifact upload. No staging-dir cleanup is
//     issued (the flow does not require it).
//   - The merge-state check runs `status` after placement and
//     before merge. status stdout containing the token "merged" (matched
//     case-insensitively; systemd 255 prints "STATUS: MERGED") means the
//     hierarchy is already merged: the provisioner issues an extra
//     `systemd-sysext unmerge` first and emits a UI warning containing
//     "merged" (TestBakeAlreadyMergedUnmergesFirst). Otherwise no pre-merge
//     unmerge and no warning (canonical test asserts the UI stays silent).
//   - On merge non-zero exit the provisioner issues one
//     `systemd-sysext status` capture command, then fails with a
//     *guestops.Error whose Code is guestops.Code("MERGE_FAILED") and whose
//     message contains the code, the merge command line, the exit code, and
//     the extension name when determinable.
//   - Bake cleanup uses `rm -rf` for extension artifacts (a directory, or
//     all .raw set basenames in one command) and `rm -f` for the baked
//     release file.
//   - No staging-dir cleanup and no enable/is-enabled commands are pinned:
//     the flow does not require them, and bake mode forbids boot-service
//     enablement. Extra commands break the exact-stream assertions.

package sysext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	guestops "github.com/eryoma/packer-plugin-systemd-ext/internal/guestops"
)

// wantSysextBakeDirectoryStream is the canonical pinned Start stream for one
// directory-format sysext extension in bake mode (see file header). The
// persist tests reuse this exact list for idempotency accounting, so any
// change to the command sequence is a contract change.
func wantSysextBakeDirectoryStream() []string {
	return []string{
		"command -v systemd-sysext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-sysext-RANDOM",
		"mv /var/tmp/packer-sysext-RANDOM/extone /var/lib/extensions/extone.tmpRANDOM",
		"mv /var/lib/extensions/extone.tmpRANDOM /var/lib/extensions/extone",
		"chown -R root:root /var/lib/extensions/extone",
		"systemd-sysext status",
		"systemd-sysext merge",
		"systemd-sysext unmerge",
		"cp -a /var/lib/extensions/extone/. /",
		"rm -rf /var/lib/extensions/extone",
		"rm -f /usr/lib/extension-release.d/extension-release.extone",
	}
}

// sysextBakeConfig returns a valid bake config with one directory-format
// extension named extone.
func sysextBakeConfig(t *testing.T) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"extensions": []map[string]interface{}{
			{"name": "extone", "source": t.TempDir()},
		},
	}
}

func TestBakeCanonicalDirectoryFlow(t *testing.T) {
	// The canonical bake run: exact ordered command stream, upload
	// destinations, the single os-release read, no boot-service enablement,
	// and no spurious already-merged warning.
	var p Provisioner
	if err := p.Prepare(sysextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		bakeDirectoryResponses("systemd-sysext", "No extensions applied.\n", 0, "")...)

	ui, buf := capturedUi()
	if err := provisionBake(t, &p, ui, comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, wantSysextBakeDirectoryStream())
	assertUploadDirs(t, comm, "/var/tmp/packer-sysext-RANDOM/extone")
	assertUploads(t, comm)                      // directory format uploads no files
	assertDownloads(t, comm, "/etc/os-release") // once per run
	assertNoEnableCommands(t, comm.commands)

	// No already-merged warning when status shows not merged; bake mode emits
	// no boot-service enablement (asserted above).
	if out := buf.String(); strings.Contains(out, "merged") {
		t.Errorf("UI output %q mentions merged although status showed not merged", out)
	}
}

func TestBakeIgnoresMergeDuringBuild(t *testing.T) {
	// merge_during_build is ignored in bake mode; the command stream must be
	// identical whether it is true or false.
	run := func(mdb bool) *bakeComm {
		t.Helper()
		var p Provisioner
		cfg := sysextBakeConfig(t)
		cfg["merge_during_build"] = mdb
		if err := p.Prepare(cfg); err != nil {
			t.Fatalf("Prepare(merge_during_build=%v): %v", mdb, err)
		}
		comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
			bakeDirectoryResponses("systemd-sysext", "No extensions applied.\n", 0, "")...)
		if err := provisionBake(t, &p, testUi(), comm); err != nil {
			t.Fatalf("Provision(merge_during_build=%v): %v", mdb, err)
		}
		return comm
	}

	gotTrue := normalizeStream(run(true).commands)
	gotFalse := normalizeStream(run(false).commands)
	want := wantSysextBakeDirectoryStream()
	if len(gotTrue) != len(want) || len(gotFalse) != len(want) {
		t.Fatalf("stream length with merge_during_build=true is %d and false is %d, want %d",
			len(gotTrue), len(gotFalse), len(want))
	}
	for i := range want {
		if gotTrue[i] != want[i] {
			t.Errorf("merge_during_build=true command %d = %q, want %q", i, gotTrue[i], want[i])
		}
		if gotFalse[i] != want[i] {
			t.Errorf("merge_during_build=false command %d = %q, want %q", i, gotFalse[i], want[i])
		}
	}
}

func TestBakeMergeFailureReturnsMergeFailed(t *testing.T) {
	// Merge non-zero exit -> capture status output, then fail with
	// MERGE_FAILED including the extension name (determinable: one entry).
	var p Provisioner
	if err := p.Prepare(sysextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		bakeDirectoryResponses("systemd-sysext", "No extensions applied.\n", 1, "Extension extone conflicts with host\n")...)

	err := provisionBake(t, &p, testUi(), comm)
	if err == nil {
		t.Fatal("Provision returned nil error, want MERGE_FAILED")
	}

	ge := requireGuestError(t, err)
	if ge.Code != guestops.Code("MERGE_FAILED") {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.Code("MERGE_FAILED"))
	}
	for _, want := range []string{"MERGE_FAILED", "extone", "systemd-sysext merge", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q does not contain %q (code, command, exit code, name)", err.Error(), want)
		}
	}

	// The stream ends at the status capture; no bake continuation.
	assertCommands(t, comm, []string{
		"command -v systemd-sysext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-sysext-RANDOM",
		"mv /var/tmp/packer-sysext-RANDOM/extone /var/lib/extensions/extone.tmpRANDOM",
		"mv /var/lib/extensions/extone.tmpRANDOM /var/lib/extensions/extone",
		"chown -R root:root /var/lib/extensions/extone",
		"systemd-sysext status",
		"systemd-sysext merge",
		"systemd-sysext status", // status capture
	})
}

func TestBakeAlreadyMergedUnmergesFirst(t *testing.T) {
	// When status reports the hierarchy already merged, bake issues an extra
	// pre-merge unmerge with a UI warning, then proceeds.
	var p Provisioner
	if err := p.Prepare(sysextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		alreadyMergedResponses("systemd-sysext")...)

	ui, buf := capturedUi()
	if err := provisionBake(t, &p, ui, comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, []string{
		"command -v systemd-sysext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-sysext-RANDOM",
		"mv /var/tmp/packer-sysext-RANDOM/extone /var/lib/extensions/extone.tmpRANDOM",
		"mv /var/lib/extensions/extone.tmpRANDOM /var/lib/extensions/extone",
		"chown -R root:root /var/lib/extensions/extone",
		"systemd-sysext status",
		"systemd-sysext unmerge", // pre-merge unmerge-if-merged
		"systemd-sysext merge",
		"systemd-sysext unmerge",
		"cp -a /var/lib/extensions/extone/. /",
		"rm -rf /var/lib/extensions/extone",
		"rm -f /usr/lib/extension-release.d/extension-release.extone",
	})
	assertNoEnableCommands(t, comm.commands)

	if out := buf.String(); !strings.Contains(out, "merged") {
		t.Errorf("UI warning %q does not mention the merged state", out)
	}
}

func TestBakeRawWithVerityCompanions(t *testing.T) {
	// A .raw source with its full verity set uploads all four artifacts and
	// copies content via systemd-dissect. No os-release read happens for a
	// .raw-only run (release generation is not needed).
	srcDir := t.TempDir()
	for _, f := range []string{"rawimg.raw", "rawimg.verity", "rawimg.roothash", "rawimg.roothash.p7s"} {
		if err := os.WriteFile(filepath.Join(srcDir, f), []byte("fixture"), 0o644); err != nil {
			t.Fatalf("write fixture %s: %v", f, err)
		}
	}
	var p Provisioner
	cfg := map[string]interface{}{
		"extensions": []map[string]interface{}{
			{"name": "rawimg", "source": filepath.Join(srcDir, "rawimg.raw")},
		},
	}
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("", rawBakeResponses("systemd-sysext", "No extensions applied.\n")...)
	if err := provisionBake(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertUploads(t, comm,
		"/var/tmp/packer-sysext-RANDOM/rawimg.raw",
		"/var/tmp/packer-sysext-RANDOM/rawimg.verity",
		"/var/tmp/packer-sysext-RANDOM/rawimg.roothash",
		"/var/tmp/packer-sysext-RANDOM/rawimg.roothash.p7s",
	)
	assertUploadDirs(t, comm) // .raw format uploads files only
	assertDownloads(t, comm)  // no directory source -> no os-release read

	assertCommands(t, comm, []string{
		"command -v systemd-sysext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-sysext-RANDOM",
		"mv /var/tmp/packer-sysext-RANDOM/rawimg.raw /var/lib/extensions/rawimg.raw.tmpRANDOM",
		"mv /var/lib/extensions/rawimg.raw.tmpRANDOM /var/lib/extensions/rawimg.raw",
		"chown root:root /var/lib/extensions/rawimg.raw",
		"mv /var/tmp/packer-sysext-RANDOM/rawimg.verity /var/lib/extensions/rawimg.verity.tmpRANDOM",
		"mv /var/lib/extensions/rawimg.verity.tmpRANDOM /var/lib/extensions/rawimg.verity",
		"chown root:root /var/lib/extensions/rawimg.verity",
		"mv /var/tmp/packer-sysext-RANDOM/rawimg.roothash /var/lib/extensions/rawimg.roothash.tmpRANDOM",
		"mv /var/lib/extensions/rawimg.roothash.tmpRANDOM /var/lib/extensions/rawimg.roothash",
		"chown root:root /var/lib/extensions/rawimg.roothash",
		"mv /var/tmp/packer-sysext-RANDOM/rawimg.roothash.p7s /var/lib/extensions/rawimg.roothash.p7s.tmpRANDOM",
		"mv /var/lib/extensions/rawimg.roothash.p7s.tmpRANDOM /var/lib/extensions/rawimg.roothash.p7s",
		"chown root:root /var/lib/extensions/rawimg.roothash.p7s",
		"systemd-sysext status",
		"systemd-sysext merge",
		"systemd-sysext unmerge",
		"systemd-dissect --copy-from /var/lib/extensions/rawimg.raw / /var/tmp/packer-bake-rawimg/",
		"cp -a /var/tmp/packer-bake-rawimg/. /",
		"rm -rf /var/tmp/packer-bake-rawimg",
		"rm -rf /var/lib/extensions/rawimg.raw /var/lib/extensions/rawimg.verity /var/lib/extensions/rawimg.roothash /var/lib/extensions/rawimg.roothash.p7s",
		"rm -f /usr/lib/extension-release.d/extension-release.rawimg",
	})
	assertNoEnableCommands(t, comm.commands)
}

func TestBakeAtomicRenameOverwritesExistingFinal(t *testing.T) {
	// Placement edge: the final name already exists on the guest (a prior
	// run, or a stale artifact). Placement must re-upload and plain-mv
	// over it (POSIX mv replaces the destination). Running twice must
	// produce two identical placement streams; the second run converges by
	// overwriting rather than failing.
	run := func() *bakeComm {
		t.Helper()
		var p Provisioner
		if err := p.Prepare(sysextBakeConfig(t)); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
			bakeDirectoryResponses("systemd-sysext", "No extensions applied.\n", 0, "")...)
		if err := provisionBake(t, &p, testUi(), comm); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		return comm
	}

	first := run()
	second := run()
	assertCommands(t, first, wantSysextBakeDirectoryStream())
	assertCommands(t, second, wantSysextBakeDirectoryStream())

	// The atomic rename is a plain "mv src dst": no -i/-n interactive or
	// no-clobber flags, no pre-existence check command.
	for _, c := range second.commands {
		if !strings.HasPrefix(c, "mv ") {
			continue
		}
		if fields := strings.Fields(c); len(fields) != 3 || fields[0] != "mv" {
			t.Errorf("atomic placement must be a plain 'mv src dst' (mv overwrites the destination); got %q", c)
		}
	}
}

func TestBakePreflightFailureStopsBeforeUpload(t *testing.T) {
	// Preflight runs first and its failure stops the run before any upload or
	// bake command.
	var p Provisioner
	if err := p.Prepare(sysextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("",
		scriptedResponse{stderr: "systemd-sysext: command not found\n", exit: 1})

	err := provisionBake(t, &p, testUi(), comm)
	if err == nil {
		t.Fatal("Provision returned nil error, want GUEST_COMMAND_NOT_FOUND")
	}
	ge := requireGuestError(t, err)
	if ge.Code != guestops.CodeGuestCommandNotFound {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.CodeGuestCommandNotFound)
	}
	if !strings.Contains(err.Error(), "GUEST_COMMAND_NOT_FOUND") {
		t.Errorf("error message %q does not contain GUEST_COMMAND_NOT_FOUND", err.Error())
	}

	assertCommands(t, comm, []string{"command -v systemd-sysext"})
	assertUploadDirs(t, comm)
	assertUploads(t, comm)
	assertDownloads(t, comm)
}

func TestBakeDoesNotEnableBootServices(t *testing.T) {
	// Bake mode never enables systemd-sysext.service, so no systemctl
	// enable / is-enabled command appears anywhere in the stream.
	var p Provisioner
	if err := p.Prepare(sysextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		bakeDirectoryResponses("systemd-sysext", "No extensions applied.\n", 0, "")...)
	if err := provisionBake(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	assertNoEnableCommands(t, comm.commands)
}
