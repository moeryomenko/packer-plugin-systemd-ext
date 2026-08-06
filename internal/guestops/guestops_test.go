// Test contract: guest preflight, guest command helpers, and machine-readable
// error codes.
//
// This file defines the intended public API of internal/guestops; the engineer
// implements it. Expected red state against the current repo:
// internal/guestops is an empty stub (doc.go only), so this file fails to
// compile with `undefined: guestops.Preflight` (and the other referenced
// symbols). That compile failure IS the TDD red phase for this task.
//
// Design notes (contract decisions the engineer implements to match):
//   - Preflight runs the three checks in order over the communicator:
//     `command -v <command>`, `systemctl --version`, `id -u`. Commands are
//     passed to comm.Start; the fake communicator below records every
//     rc.Command so tests assert the exact issued sequence.
//   - The command helpers return non-shell arg slices (no "sh -c", no
//     metacharacters); they never execute anything.
//   - Failures are typed errors carrying a stable machine-readable Code that
//     is also embedded in the error message.
//   - VersionInfo.Raw is the first line of `systemctl --version` output
//     (trailing whitespace trimmed); Major is the parsed major systemd
//     version, accepting both "systemd 255 (...)" and "systemd v254 (...)"
//     output formats.

package guestops_test

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"

	guestops "github.com/eryoma/packer-plugin-systemd-ext/internal/guestops"
)

// ---------------------------------------------------------------------------
// Scripted fake communicator
// ---------------------------------------------------------------------------

// scriptedResponse is the canned output/exit for one Start call.
type scriptedResponse struct {
	stdout string
	stderr string
	exit   int
}

// scriptedComm is a scripted fake packersdk.Communicator. Every Start call is
// recorded in order and answered from a FIFO response table. An unscripted
// Start call is recorded and flagged (so the test fails) but still exits with
// status 1 so the caller never hangs. Upload/Download methods are inherited
// from the SDK MockCommunicator (unused by preflight).
type scriptedComm struct {
	*packersdk.MockCommunicator
	mu        sync.Mutex
	responses []scriptedResponse
	commands  []string
	extra     bool
}

var _ packersdk.Communicator = (*scriptedComm)(nil)

func newScriptedComm(responses ...scriptedResponse) *scriptedComm {
	return &scriptedComm{
		MockCommunicator: new(packersdk.MockCommunicator),
		responses:        responses,
	}
}

func (c *scriptedComm) Start(_ context.Context, rc *packersdk.RemoteCmd) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands = append(c.commands, rc.Command)

	resp := scriptedResponse{exit: 1}
	if len(c.responses) > 0 {
		resp = c.responses[0]
		c.responses = c.responses[1:]
	} else {
		c.extra = true
	}

	go func() {
		if rc.Stdout != nil && resp.stdout != "" {
			_, _ = io.Copy(rc.Stdout, strings.NewReader(resp.stdout))
		}
		if rc.Stderr != nil && resp.stderr != "" {
			_, _ = io.Copy(rc.Stderr, strings.NewReader(resp.stderr))
		}
		rc.SetExited(resp.exit)
	}()
	return nil
}

// wantCommands asserts the exact ordered list of commands issued through
// comm.Start, using the same arg-slice constructors the production provisioner
// is expected to use so the assertion tracks the construction contract.
func wantCommands(command string) []string {
	return []string{
		strings.Join(guestops.FindCommandArgs(command), " "),
		strings.Join(guestops.SystemctlVersionArgs(), " "),
		"id -u",
	}
}

func assertCommands(t *testing.T, comm *scriptedComm, want []string) {
	t.Helper()
	if comm.extra {
		t.Error("fake communicator received more Start calls than scripted responses; preflight issued an unexpected command")
	}
	if len(comm.commands) != len(want) {
		t.Errorf("issued %d commands, want %d:\n got: %q\nwant: %q", len(comm.commands), len(want), comm.commands, want)
		return
	}
	for i := range want {
		if comm.commands[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, comm.commands[i], want[i])
		}
	}
}

func testUi() packersdk.Ui {
	return &packersdk.BasicUi{
		Reader:      strings.NewReader(""),
		Writer:      io.Discard,
		ErrorWriter: io.Discard,
	}
}

// requireGuestError extracts the typed *guestops.Error from err via errors.As.
func requireGuestError(t *testing.T, err error) *guestops.Error {
	t.Helper()
	if err == nil {
		t.Fatal("Preflight returned nil error, want typed error")
	}
	var ge *guestops.Error
	if !errors.As(err, &ge) {
		t.Fatalf("error %q is not (or does not wrap) a *guestops.Error", err)
	}
	return ge
}

// ---------------------------------------------------------------------------
// Command helpers: non-shell arg slice construction
// ---------------------------------------------------------------------------

func TestCommandHelpersBuildNonShellArgSlices(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{name: "command -v", args: guestops.FindCommandArgs("systemd-sysext"), want: []string{"command", "-v", "systemd-sysext"}},
		{name: "systemctl --version", args: guestops.SystemctlVersionArgs(), want: []string{"systemctl", "--version"}},
		{name: "status", args: guestops.StatusArgs("systemd-sysext"), want: []string{"systemd-sysext", "status"}},
		{name: "merge", args: guestops.MergeArgs("systemd-sysext"), want: []string{"systemd-sysext", "merge"}},
		{name: "unmerge", args: guestops.UnmergeArgs("systemd-sysext"), want: []string{"systemd-sysext", "unmerge"}},
		{name: "enable", args: guestops.EnableArgs("systemd-sysext.service"), want: []string{"systemctl", "enable", "systemd-sysext.service"}},
		{name: "is-enabled", args: guestops.IsEnabledArgs("systemd-sysext.service"), want: []string{"systemctl", "is-enabled", "systemd-sysext.service"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if len(tt.args) != len(tt.want) {
				t.Fatalf("args = %q, want %q", tt.args, tt.want)
			}
			for i := range tt.want {
				if tt.args[i] != tt.want[i] {
					t.Errorf("args[%d] = %q, want %q", i, tt.args[i], tt.want[i])
				}
			}
			// No shell: the constructed slice is plain argv — no sh -c wrapper,
			// no pipelines, no redirection, no command substitution.
			joined := strings.Join(tt.args, " ")
			for _, token := range []string{"sh -c", "&&", "||", ";", "|", ">", "<", "`", "$("} {
				if strings.Contains(joined, token) {
					t.Errorf("args %q contain shell construct %q; helpers must build non-shell argv", tt.args, token)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Preflight success
// ---------------------------------------------------------------------------

func TestPreflightSuccess(t *testing.T) {
	tests := []struct {
		name        string
		typ         guestops.ExtensionType
		command     string
		versionLine string
		wantMajor   int
		wantRaw     string
	}{
		{
			name:        "sysext ubuntu format",
			typ:         guestops.TypeSysext,
			command:     "systemd-sysext",
			versionLine: "systemd 255 (255.4-1ubuntu8)\n-PAM -AUDIT -SELINUX\n",
			wantMajor:   255,
			wantRaw:     "systemd 255 (255.4-1ubuntu8)",
		},
		{
			name:        "sysext v-prefixed debian format",
			typ:         guestops.TypeSysext,
			command:     "systemd-sysext",
			versionLine: "systemd v254 (254.10-1~bpo12+1)\n",
			wantMajor:   254,
			wantRaw:     "systemd v254 (254.10-1~bpo12+1)",
		},
		{
			name:        "sysext boundary exactly 252",
			typ:         guestops.TypeSysext,
			command:     "systemd-sysext",
			versionLine: "systemd 252 (252.26-1)\n",
			wantMajor:   252,
			wantRaw:     "systemd 252 (252.26-1)",
		},
		{
			name:        "confext boundary exactly 254",
			typ:         guestops.TypeConfext,
			command:     "systemd-confext",
			versionLine: "systemd 254 (254.5-1)\n",
			wantMajor:   254,
			wantRaw:     "systemd 254 (254.5-1)",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comm := newScriptedComm(
				scriptedResponse{stdout: "/usr/bin/" + tt.command + "\n"}, // command -v
				scriptedResponse{stdout: tt.versionLine},                  // systemctl --version
				scriptedResponse{stdout: "0\n"},                           // id -u
			)
			info, err := guestops.Preflight(t.Context(), testUi(), comm, tt.typ, tt.command)
			if err != nil {
				t.Fatalf("Preflight(...) = error %v, want nil", err)
			}
			if info.Major != tt.wantMajor {
				t.Errorf("VersionInfo.Major = %d, want %d", info.Major, tt.wantMajor)
			}
			if info.Raw != tt.wantRaw {
				t.Errorf("VersionInfo.Raw = %q, want %q", info.Raw, tt.wantRaw)
			}
			assertCommands(t, comm, wantCommands(tt.command))
		})
	}
}

// command -v exit 0 with empty stdout: resolution is exit-status based, so the
// check passes and preflight proceeds to the version and root checks.
func TestPreflightCommandVEmptyStdout(t *testing.T) {
	comm := newScriptedComm(
		scriptedResponse{stdout: "", exit: 0},                      // command -v: found (empty output)
		scriptedResponse{stdout: "systemd 255 (255.4-1ubuntu8)\n"}, // systemctl --version
		scriptedResponse{stdout: "0\n"},                            // id -u
	)
	if _, err := guestops.Preflight(t.Context(), testUi(), comm, guestops.TypeSysext, "systemd-sysext"); err != nil {
		t.Fatalf("Preflight(...) = error %v, want nil (exit 0 means resolved)", err)
	}
	assertCommands(t, comm, wantCommands("systemd-sysext"))
}

// ---------------------------------------------------------------------------
// Preflight failure paths (error codes + message semantics)
// ---------------------------------------------------------------------------

func TestPreflightGuestCommandNotFound(t *testing.T) {
	comm := newScriptedComm(
		scriptedResponse{stderr: "systemd-sysext: command not found\n", exit: 1}, // command -v
	)
	_, err := guestops.Preflight(t.Context(), testUi(), comm, guestops.TypeSysext, "systemd-sysext")

	ge := requireGuestError(t, err)
	if ge.Code != guestops.CodeGuestCommandNotFound {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.CodeGuestCommandNotFound)
	}
	for _, want := range []string{"GUEST_COMMAND_NOT_FOUND", "systemd-sysext", "command not found"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error message %q does not contain %q (code, command, stderr tail)", err.Error(), want)
		}
	}
	// Preflight stops at the first failing check.
	assertCommands(t, comm, []string{strings.Join(guestops.FindCommandArgs("systemd-sysext"), " ")})
}

func TestPreflightSystemdTooOld(t *testing.T) {
	tests := []struct {
		name         string
		typ          guestops.ExtensionType
		command      string
		versionLine  string
		wantDetected string
	}{
		{
			name:         "sysext below 252",
			typ:          guestops.TypeSysext,
			command:      "systemd-sysext",
			versionLine:  "systemd 251 (251.4-1ubuntu8)\n",
			wantDetected: "251",
		},
		{
			name:         "confext below 254",
			typ:          guestops.TypeConfext,
			command:      "systemd-confext",
			versionLine:  "systemd 253 (253.13-1ubuntu1.1)\n",
			wantDetected: "253",
		},
		{
			name:         "sysext v-prefixed below floor",
			typ:          guestops.TypeSysext,
			command:      "systemd-sysext",
			versionLine:  "systemd v249\n",
			wantDetected: "249",
		},
		{
			name:         "unparseable version line",
			typ:          guestops.TypeSysext,
			command:      "systemd-sysext",
			versionLine:  "systemd foo\n",
			wantDetected: "systemd foo",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			comm := newScriptedComm(
				scriptedResponse{stdout: "/usr/bin/" + tt.command + "\n"}, // command -v
				scriptedResponse{stdout: tt.versionLine},                  // systemctl --version
			)
			_, err := guestops.Preflight(t.Context(), testUi(), comm, tt.typ, tt.command)

			ge := requireGuestError(t, err)
			if ge.Code != guestops.CodeSystemdTooOld {
				t.Errorf("error code = %q, want %q", ge.Code, guestops.CodeSystemdTooOld)
			}
			for _, want := range []string{"SYSTEMD_TOO_OLD", tt.wantDetected} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error message %q does not contain %q (code + detected version)", err.Error(), want)
				}
			}
			// Version check fails before the root check; only two commands run.
			assertCommands(t, comm, wantCommands(tt.command)[:2])
		})
	}
}

func TestPreflightNotRoot(t *testing.T) {
	comm := newScriptedComm(
		scriptedResponse{stdout: "/usr/bin/systemd-sysext\n"},      // command -v
		scriptedResponse{stdout: "systemd 255 (255.4-1ubuntu8)\n"}, // systemctl --version
		scriptedResponse{stdout: "1000\n", exit: 0},                // id -u: non-root
	)
	_, err := guestops.Preflight(t.Context(), testUi(), comm, guestops.TypeSysext, "systemd-sysext")

	ge := requireGuestError(t, err)
	if ge.Code != guestops.CodeNotRoot {
		t.Errorf("error code = %q, want %q", ge.Code, guestops.CodeNotRoot)
	}
	if !strings.Contains(err.Error(), "NOT_ROOT") {
		t.Errorf("error message %q does not contain NOT_ROOT", err.Error())
	}
	// All three checks run; the root check is the last one.
	assertCommands(t, comm, wantCommands("systemd-sysext"))
}
