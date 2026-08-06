// Test contract: the persist flow and idempotency for the confext provisioner,
// plus the persist portion of the e2e contract. The engineer implements
// Provision's persist path to match these tests exactly. Expected red state
// against the current code: Provision returns "confext provisioner: mode
// \"persist\": persist not yet implemented" before issuing any guest command,
// so every test fails on behavior (no compile failure — the package layout and
// the fake communicator are final).
//
// This file mirrors provisioner/sysext/provisioner_persist_test.go with the
// confext-specific contract: command systemd-confext, install dir
// /var/lib/confexts, staging /var/tmp/packer-confext-<random>/, and boot
// service systemd-confext.service. The pinned conventions (preflight first,
// bake-identical placement, merge-state check, enable + is-enabled
// verification, no merge when merge_during_build=false, no rm in persist,
// per-run idempotency) are identical to the sysext contract — see the sysext
// file header for the full rationale.

package confext

import (
	"strings"
	"testing"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"

	guestops "github.com/eryoma/packer-plugin-systemd-ext/internal/guestops"
)

// wantConfextPersistMergeStream is the canonical pinned Start stream for one
// directory-format confext extension in persist mode with merge_during_build
// true (the default) when status reports not merged.
func wantConfextPersistMergeStream() []string {
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
		"systemctl enable systemd-confext.service",
		"systemctl is-enabled systemd-confext.service",
	}
}

// wantConfextPersistUnmergeMergeStream is the persist stream when status
// reports the hierarchy already merged: the extra pre-merge unmerge with a
// UI warning before the merge.
func wantConfextPersistUnmergeMergeStream() []string {
	return []string{
		"command -v systemd-confext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-confext-RANDOM",
		"mv /var/tmp/packer-confext-RANDOM/extone /var/lib/confexts/extone.tmpRANDOM",
		"mv /var/lib/confexts/extone.tmpRANDOM /var/lib/confexts/extone",
		"chown -R root:root /var/lib/confexts/extone",
		"systemd-confext status",
		"systemd-confext unmerge", // unmerge-if-merged
		"systemd-confext merge",
		"systemctl enable systemd-confext.service",
		"systemctl is-enabled systemd-confext.service",
	}
}

// wantConfextPersistNoMergeStream is the persist stream when
// merge_during_build=false: no status/merge/unmerge commands at all;
// placement then service enablement.
func wantConfextPersistNoMergeStream() []string {
	return []string{
		"command -v systemd-confext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-confext-RANDOM",
		"mv /var/tmp/packer-confext-RANDOM/extone /var/lib/confexts/extone.tmpRANDOM",
		"mv /var/lib/confexts/extone.tmpRANDOM /var/lib/confexts/extone",
		"chown -R root:root /var/lib/confexts/extone",
		"systemctl enable systemd-confext.service",
		"systemctl is-enabled systemd-confext.service",
	}
}

// confextPersistConfig returns a valid persist-mode config with one
// directory-format extension named extone. merge_during_build defaults to
// true; tests override it for the false path.
func confextPersistConfig(t *testing.T) map[string]interface{} {
	t.Helper()
	cfg := confextBakeConfig(t)
	cfg["mode"] = "persist"
	return cfg
}

// provisionPersist runs Provision for a persist-mode config with the given UI
// and fake communicator.
func provisionPersist(t *testing.T, p *Provisioner, ui packersdk.Ui, comm *bakeComm) error {
	t.Helper()
	return p.Provision(t.Context(), ui, comm, nil)
}

// ---------------------------------------------------------------------------
// Scripted response builders for the persist flow
// ---------------------------------------------------------------------------

// persistDirectoryResponses scripts a directory-format persist run with
// merge_during_build=true: preflight, placement (mv/mv/chown), the merge-state
// status, an optional unmerge-if-merged when status reports merged, the merge,
// then the enable + is-enabled verification.
// statusStdout is the status output; alreadyMerged selects the pre-merge
// unmerge. isEnabledStdout/Exit/Stderr script the is-enabled verification.
func persistDirectoryResponses(command, statusStdout string, alreadyMerged bool, isEnabledStdout string, isEnabledExit int, isEnabledStderr string) []scriptedResponse {
	resp := preflightResponses(command)
	resp = append(resp, placementResponses(1)...)
	resp = append(resp, scriptedResponse{stdout: statusStdout})
	if alreadyMerged {
		resp = append(resp, scriptedResponse{}) // unmerge-if-merged
	}
	resp = append(resp,
		scriptedResponse{}, // merge
		scriptedResponse{}, // systemctl enable
		scriptedResponse{stdout: isEnabledStdout, stderr: isEnabledStderr, exit: isEnabledExit}, // systemctl is-enabled
	)
	return resp
}

// persistNoMergeResponses scripts a persist run with
// merge_during_build=false: preflight, placement, then only the enable +
// is-enabled verification (no status/merge/unmerge at build time).
func persistNoMergeResponses(command string) []scriptedResponse {
	resp := preflightResponses(command)
	resp = append(resp, placementResponses(1)...)
	resp = append(resp,
		scriptedResponse{},                    // systemctl enable
		scriptedResponse{stdout: "enabled\n"}, // systemctl is-enabled
	)
	return resp
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

// assertNoRemovalCommands fails if any command is an rm invocation.
// Persist leaves the artifacts in guest_install_dir, so no artifact removal
// (bake-only) and no release-file removal (bake-only) may appear.
func assertNoRemovalCommands(t *testing.T, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		if fields := strings.Fields(c); len(fields) > 0 && fields[0] == "rm" {
			t.Errorf("persist mode issued removal command %q (artifacts remain in guest_install_dir)", c)
		}
	}
}

// assertNoMergeStateCommands fails if any command is a status/merge/unmerge
// of the extension binary. merge_during_build=false issues no merge commands;
// the boot service performs the merge at first boot.
func assertNoMergeStateCommands(t *testing.T, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		for _, tok := range strings.Fields(c) {
			if tok == "merge" || tok == "unmerge" || tok == "status" {
				t.Errorf("merge_during_build=false issued merge-state command %q", c)
			}
		}
	}
}

// countToken returns the number of commands containing tok as a whitespace
// token. Tokens are whole words, so "enable" does not match "is-enabled".
func countToken(cmds []string, tok string) int {
	n := 0
	for _, c := range cmds {
		for _, f := range strings.Fields(c) {
			if f == tok {
				n++
			}
		}
	}
	return n
}

// ---------------------------------------------------------------------------
// Persist flow tests
// ---------------------------------------------------------------------------

func TestPersistCanonicalDirectoryFlow(t *testing.T) {
	// The canonical confext persist run with the default merge_during_build
	// (true) and status reporting not merged: exact ordered command stream,
	// bake-identical placement, boot-service enablement + verification, no
	// artifact removal, and no spurious already-merged warning.
	var p Provisioner
	if err := p.Prepare(confextPersistConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		persistDirectoryResponses("systemd-confext", "No extensions applied.\n", false, "enabled\n", 0, "")...)

	ui, buf := capturedUi()
	if err := provisionPersist(t, &p, ui, comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, wantConfextPersistMergeStream())
	assertUploadDirs(t, comm, "/var/tmp/packer-confext-RANDOM/extone")
	assertUploads(t, comm)                      // directory format uploads no files
	assertDownloads(t, comm, "/etc/os-release") // once per run
	assertNoRemovalCommands(t, comm.commands)

	// No already-merged warning when status shows not merged.
	if out := buf.String(); strings.Contains(out, "merged") {
		t.Errorf("UI output %q mentions merged although status showed not merged", out)
	}
}

func TestPersistAlreadyMergedUnmergesFirst(t *testing.T) {
	// When status reports the hierarchy already merged, persist issues an
	// extra pre-merge unmerge with a UI warning, then merges and enables the
	// boot service.
	var p Provisioner
	if err := p.Prepare(confextPersistConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		persistDirectoryResponses("systemd-confext", "extone is merged\n", true, "enabled\n", 0, "")...)

	ui, buf := capturedUi()
	if err := provisionPersist(t, &p, ui, comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, wantConfextPersistUnmergeMergeStream())
	assertNoRemovalCommands(t, comm.commands)

	if out := buf.String(); !strings.Contains(out, "merged") {
		t.Errorf("UI warning %q does not mention the merged state", out)
	}
}

func TestPersistNoMergeWhenMergeDuringBuildFalse(t *testing.T) {
	// merge_during_build=false issues no status/merge/unmerge commands; the
	// flow is placement then service enablement. The boot service performs
	// the merge at first boot.
	var p Provisioner
	cfg := confextPersistConfig(t)
	cfg["merge_during_build"] = false
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		persistNoMergeResponses("systemd-confext")...)

	if err := provisionPersist(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertCommands(t, comm, wantConfextPersistNoMergeStream())
	assertNoMergeStateCommands(t, comm.commands)
	assertNoRemovalCommands(t, comm.commands)
	assertUploadDirs(t, comm, "/var/tmp/packer-confext-RANDOM/extone")
	assertDownloads(t, comm, "/etc/os-release")

	// Enablement still happens even without a build-time merge.
	if got := countToken(comm.commands, "enable"); got != 1 {
		t.Errorf("issued %d 'enable' tokens, want exactly 1", got)
	}
}

func TestPersistServiceEnableFailed(t *testing.T) {
	// The systemctl is-enabled verification fails on a non-zero exit (the
	// realistic "disabled" variant) -> SERVICE_ENABLE_FAILED, with the code,
	// the command line, and the exit code in the message.
	var p Provisioner
	if err := p.Prepare(confextPersistConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		persistDirectoryResponses("systemd-confext", "No extensions applied.\n", false, "disabled\n", 1, "")...)

	err := provisionPersist(t, &p, testUi(), comm)
	if err == nil {
		t.Fatal("Provision returned nil error, want SERVICE_ENABLE_FAILED")
	}
	ge := requireGuestError(t, err)
	if ge.Code != guestops.Code("SERVICE_ENABLE_FAILED") {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.Code("SERVICE_ENABLE_FAILED"))
	}
	for _, want := range []string{"SERVICE_ENABLE_FAILED", "systemctl is-enabled systemd-confext.service", "1"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q does not contain %q (code, command, exit code)", err.Error(), want)
		}
	}

	// The stream runs the full persist flow up to the failed verification.
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
		"systemctl enable systemd-confext.service",
		"systemctl is-enabled systemd-confext.service",
	})
}

func TestPersistPreflightFailureStopsBeforeUpload(t *testing.T) {
	// Persist mode runs the same preflight as bake and stops before any
	// upload, placement, merge, or enablement when a check fails.
	var p Provisioner
	if err := p.Prepare(confextPersistConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("",
		scriptedResponse{stderr: "systemd-confext: command not found\n", exit: 1})

	err := provisionPersist(t, &p, testUi(), comm)
	if err == nil {
		t.Fatal("Provision returned nil error, want GUEST_COMMAND_NOT_FOUND")
	}
	ge := requireGuestError(t, err)
	if ge.Code != guestops.CodeGuestCommandNotFound {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.CodeGuestCommandNotFound)
	}

	assertCommands(t, comm, []string{"command -v systemd-confext"})
	assertUploadDirs(t, comm)
	assertUploads(t, comm)
	assertDownloads(t, comm)
}

func TestPersistImagesRemainInInstallDir(t *testing.T) {
	// Persist leaves the extension artifacts in guest_install_dir for
	// boot-time activation. Unlike bake, no artifact removal and no
	// release-file removal may be issued.
	var p Provisioner
	if err := p.Prepare(confextPersistConfig(t)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
		persistDirectoryResponses("systemd-confext", "No extensions applied.\n", false, "enabled\n", 0, "")...)
	if err := provisionPersist(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	assertNoRemovalCommands(t, comm.commands)
}

// ---------------------------------------------------------------------------
// Idempotency tests
// ---------------------------------------------------------------------------

func TestPersistIdempotentReplaySameStream(t *testing.T) {
	// Replaying the full persist run twice (merge_during_build=true, status
	// reporting not merged both times) yields the same command stream each
	// run: exactly one merge per run (two across both runs — per-run stream
	// equality per the @build clarification, not a global single merge), no
	// unmerge, and exactly one systemctl enable + is-enabled per run. The
	// second run still enables the already-enabled unit — enablement is
	// always issued and never accumulates beyond one per run.
	run := func() *bakeComm {
		t.Helper()
		var p Provisioner
		if err := p.Prepare(confextPersistConfig(t)); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
			persistDirectoryResponses("systemd-confext", "No extensions applied.\n", false, "enabled\n", 0, "")...)
		if err := provisionPersist(t, &p, testUi(), comm); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		return comm
	}

	first := run()
	second := run()
	assertCommands(t, first, wantConfextPersistMergeStream())
	assertCommands(t, second, wantConfextPersistMergeStream())

	for _, r := range []struct {
		name string
		cmds []string
	}{{"run 1", first.commands}, {"run 2", second.commands}} {
		if got := countToken(r.cmds, "merge"); got != 1 {
			t.Errorf("%s issued %d merge commands, want exactly 1", r.name, got)
		}
		if got := countToken(r.cmds, "unmerge"); got != 0 {
			t.Errorf("%s issued %d unmerge commands, want 0 when status shows not merged", r.name, got)
		}
		if got := countToken(r.cmds, "enable"); got != 1 {
			t.Errorf("%s issued %d enable tokens, want exactly 1 (no accumulation)", r.name, got)
		}
		if got := countToken(r.cmds, "is-enabled"); got != 1 {
			t.Errorf("%s issued %d is-enabled tokens, want exactly 1", r.name, got)
		}
	}

	// Two runs -> two merges total, zero unmerges.
	all := append([]string{}, first.commands...)
	all = append(all, second.commands...)
	if got := countToken(all, "merge"); got != 2 {
		t.Errorf("two runs issued %d merge commands total, want 2 (one per run)", got)
	}
}

func TestPersistIdempotentMergedStateConverges(t *testing.T) {
	// Edge: status reports merged on both runs; each run issues exactly one
	// pre-merge unmerge + one merge (no spurious extra unmerge) and the two
	// runs are byte-for-byte identical.
	run := func() *bakeComm {
		t.Helper()
		var p Provisioner
		if err := p.Prepare(confextPersistConfig(t)); err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		comm := newBakeComm("ID=ubuntu\nVERSION_ID=24.04\n",
			persistDirectoryResponses("systemd-confext", "extone is merged\n", true, "enabled\n", 0, "")...)
		if err := provisionPersist(t, &p, testUi(), comm); err != nil {
			t.Fatalf("Provision: %v", err)
		}
		return comm
	}

	first := run()
	second := run()
	assertCommands(t, first, wantConfextPersistUnmergeMergeStream())
	assertCommands(t, second, wantConfextPersistUnmergeMergeStream())

	for _, r := range []struct {
		name string
		cmds []string
	}{{"run 1", first.commands}, {"run 2", second.commands}} {
		if got := countToken(r.cmds, "unmerge"); got != 1 {
			t.Errorf("%s issued %d unmerge commands, want exactly 1 (no spurious extra unmerge)", r.name, got)
		}
		if got := countToken(r.cmds, "merge"); got != 1 {
			t.Errorf("%s issued %d merge commands, want exactly 1", r.name, got)
		}
		if got := countToken(r.cmds, "enable"); got != 1 {
			t.Errorf("%s issued %d enable tokens, want exactly 1", r.name, got)
		}
	}
}
