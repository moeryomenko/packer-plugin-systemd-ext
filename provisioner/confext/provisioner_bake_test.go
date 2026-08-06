// Test contract: upload/atomic placement and the bake flow for the confext
// provisioner, plus the bake portion of the e2e contract. The engineer
// implements Provision's bake path to match these tests exactly. Expected red
// state against the current stub: Provision returns "confext provisioner: not
// implemented", so every test fails on behavior (no compile failure — the
// package layout is final).
//
// This file mirrors provisioner/sysext/provisioner_bake_test.go with the
// confext-specific contract: command systemd-confext, install dir
// /var/lib/confexts, staging /var/tmp/packer-confext-<random>/, and release
// cleanup at /etc/extension-release.d/extension-release.<name> (the confext
// path). The pinned conventions (atomic placement, ownership,
// os-release read, merge-state check, MERGE_FAILED semantics, rm forms) are
// identical to the sysext contract — see the sysext file header.

package confext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	guestops "github.com/eryoma/packer-plugin-systemd-ext/internal/guestops"
)

// wantConfextBakeDirectoryStream is the canonical pinned Start stream for one
// directory-format confext extension in bake mode. The persist tests reuse
// this exact list for idempotency accounting, so any change to the command
// sequence is a contract change.
func wantConfextBakeDirectoryStream() []string {
	return []string{
		"command -v systemd-confext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-confext-RANDOM",
		"mv /var/tmp/packer-confext-RANDOM/extone /var/lib/confexts/extone.tmpRANDOM",
		"mv /var/lib/confexts/extone.tmpRANDOM /var/lib/confexts/extone",
		"chown -R root:root /var/lib/confexts/extone",
		"systemd-confext status",
		"systemd-confext merge",
		"systemd-confext unmerge",
		"cp -a /var/lib/confexts/extone/. /",
		"rm -rf /var/lib/confexts/extone",
		"rm -f /etc/extension-release.d/extension-release.extone",
	}
}

// confextBakeConfig returns a valid bake config with one directory-format
// extension named extone.
func confextBakeConfig(t *testing.T) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"extensions": []map[string]interface{}{
			{"name": "extone", "source": t.TempDir()},
		},
	}
}

func TestBakeCanonicalDirectoryFlow(t *testing.T) {
	// The canonical confext bake run: exact ordered command stream, upload
	// destinations, the single os-release read, no boot-service enablement,
	// and no spurious already-merged warning. The release-file cleanup path
	// is the confext one: /etc/...
	var p Provisioner
	if err := p.Prepare(confextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		bakeDirectoryResponses("systemd-confext", "No extensions applied.\n", 0, "")...)

	ui, buf := capturedUi()
	if err := provisionBake(t, &p, ui, comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, wantConfextBakeDirectoryStream())
	assertUploadDirs(t, comm, "/var/tmp/packer-confext-RANDOM/extone")
	assertUploads(t, comm)                      // directory format uploads no files
	assertDownloads(t, comm, "/etc/os-release") // once per run
	assertNoEnableCommands(t, comm.commands)

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
		cfg := confextBakeConfig(t)
		cfg["merge_during_build"] = mdb
		if err := p.Prepare(cfg); err != nil {
			t.Fatalf("Prepare(merge_during_build=%v): %v", mdb, err)
		}
		comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
			bakeDirectoryResponses("systemd-confext", "No extensions applied.\n", 0, "")...)
		if err := provisionBake(t, &p, testUi(), comm); err != nil {
			t.Fatalf("Provision(merge_during_build=%v): %v", mdb, err)
		}
		return comm
	}

	gotTrue := normalizeStream(run(true).commands)
	gotFalse := normalizeStream(run(false).commands)
	want := wantConfextBakeDirectoryStream()
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
	if err := p.Prepare(confextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		bakeDirectoryResponses("systemd-confext", "No extensions applied.\n", 1, "Extension extone conflicts with host\n")...)

	err := provisionBake(t, &p, testUi(), comm)
	if err == nil {
		t.Fatal("Provision returned nil error, want MERGE_FAILED")
	}

	ge := requireGuestError(t, err)
	if ge.Code != guestops.Code("MERGE_FAILED") {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.Code("MERGE_FAILED"))
	}
	for _, want := range []string{"MERGE_FAILED", "extone", "systemd-confext merge", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q does not contain %q (code, command, exit code, name)", err.Error(), want)
		}
	}

	assertCommands(t, comm, []string{
		"command -v systemd-confext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-confext-RANDOM",
		"mv /var/tmp/packer-confext-RANDOM/extone /var/lib/confexts/extone.tmpRANDOM",
		"mv /var/lib/confexts/extone.tmpRANDOM /var/lib/confexts/extone",
		"chown -R root:root /var/lib/confexts/extone",
		"systemd-confext status",
		"systemd-confext merge",
		"systemd-confext status", // status capture
	})
}

func TestBakeAlreadyMergedUnmergesFirst(t *testing.T) {
	// When status reports the hierarchy already merged, bake issues an extra
	// pre-merge unmerge with a UI warning, then proceeds.
	var p Provisioner
	if err := p.Prepare(confextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		alreadyMergedResponses("systemd-confext")...)

	ui, buf := capturedUi()
	if err := provisionBake(t, &p, ui, comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, []string{
		"command -v systemd-confext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-confext-RANDOM",
		"mv /var/tmp/packer-confext-RANDOM/extone /var/lib/confexts/extone.tmpRANDOM",
		"mv /var/lib/confexts/extone.tmpRANDOM /var/lib/confexts/extone",
		"chown -R root:root /var/lib/confexts/extone",
		"systemd-confext status",
		"systemd-confext unmerge", // pre-merge unmerge-if-merged
		"systemd-confext merge",
		"systemd-confext unmerge",
		"cp -a /var/lib/confexts/extone/. /",
		"rm -rf /var/lib/confexts/extone",
		"rm -f /etc/extension-release.d/extension-release.extone",
	})
	assertNoEnableCommands(t, comm.commands)

	if out := buf.String(); !strings.Contains(out, "merged") {
		t.Errorf("UI warning %q does not mention the merged state", out)
	}
}

func TestBakeRawWithVerityCompanions(t *testing.T) {
	// A .raw source with its full verity set uploads all four artifacts and
	// copies content via systemd-dissect. No os-release read happens for a
	// .raw-only run.
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
	comm := newBakeComm("", rawBakeResponses("systemd-confext", "No extensions applied.\n")...)
	if err := provisionBake(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertUploads(t, comm,
		"/var/tmp/packer-confext-RANDOM/rawimg.raw",
		"/var/tmp/packer-confext-RANDOM/rawimg.verity",
		"/var/tmp/packer-confext-RANDOM/rawimg.roothash",
		"/var/tmp/packer-confext-RANDOM/rawimg.roothash.p7s",
	)
	assertUploadDirs(t, comm) // .raw format uploads files only
	assertDownloads(t, comm)  // no directory source -> no os-release read

	assertCommands(t, comm, []string{
		"command -v systemd-confext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-confext-RANDOM",
		"mv /var/tmp/packer-confext-RANDOM/rawimg.raw /var/lib/confexts/rawimg.raw.tmpRANDOM",
		"mv /var/lib/confexts/rawimg.raw.tmpRANDOM /var/lib/confexts/rawimg.raw",
		"chown root:root /var/lib/confexts/rawimg.raw",
		"mv /var/tmp/packer-confext-RANDOM/rawimg.verity /var/lib/confexts/rawimg.verity.tmpRANDOM",
		"mv /var/lib/confexts/rawimg.verity.tmpRANDOM /var/lib/confexts/rawimg.verity",
		"chown root:root /var/lib/confexts/rawimg.verity",
		"mv /var/tmp/packer-confext-RANDOM/rawimg.roothash /var/lib/confexts/rawimg.roothash.tmpRANDOM",
		"mv /var/lib/confexts/rawimg.roothash.tmpRANDOM /var/lib/confexts/rawimg.roothash",
		"chown root:root /var/lib/confexts/rawimg.roothash",
		"mv /var/tmp/packer-confext-RANDOM/rawimg.roothash.p7s /var/lib/confexts/rawimg.roothash.p7s.tmpRANDOM",
		"mv /var/lib/confexts/rawimg.roothash.p7s.tmpRANDOM /var/lib/confexts/rawimg.roothash.p7s",
		"chown root:root /var/lib/confexts/rawimg.roothash.p7s",
		"systemd-confext status",
		"systemd-confext merge",
		"systemd-confext unmerge",
		"systemd-dissect --copy-from /var/lib/confexts/rawimg.raw / /var/tmp/packer-bake-rawimg/",
		"cp -a /var/tmp/packer-bake-rawimg/. /",
		"rm -rf /var/tmp/packer-bake-rawimg",
		"rm -rf /var/lib/confexts/rawimg.raw /var/lib/confexts/rawimg.verity /var/lib/confexts/rawimg.roothash /var/lib/confexts/rawimg.roothash.p7s",
		"rm -f /etc/extension-release.d/extension-release.rawimg",
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
		if err := p.Prepare(confextBakeConfig(t)); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
			bakeDirectoryResponses("systemd-confext", "No extensions applied.\n", 0, "")...)
		if err := provisionBake(t, &p, testUi(), comm); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		return comm
	}

	first := run()
	second := run()
	assertCommands(t, first, wantConfextBakeDirectoryStream())
	assertCommands(t, second, wantConfextBakeDirectoryStream())

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
	if err := p.Prepare(confextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("",
		scriptedResponse{stderr: "systemd-confext: command not found\n", exit: 1})

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

	assertCommands(t, comm, []string{"command -v systemd-confext"})
	assertUploadDirs(t, comm)
	assertUploads(t, comm)
	assertDownloads(t, comm)
}

func TestBakeDoesNotEnableBootServices(t *testing.T) {
	// Bake mode never enables systemd-confext.service, so no systemctl
	// enable / is-enabled command appears anywhere in the stream.
	var p Provisioner
	if err := p.Prepare(confextBakeConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		bakeDirectoryResponses("systemd-confext", "No extensions applied.\n", 0, "")...)
	if err := provisionBake(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	assertNoEnableCommands(t, comm.commands)
}
