package guestops

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// ExtensionType identifies the provisioner kind. It selects the systemd
// version floor enforced by Preflight: sysext requires >= 252, confext
// requires >= 254.
type ExtensionType string

const (
	// TypeSysext is the systemd-sysext provisioner (merges over /usr).
	TypeSysext ExtensionType = "sysext"
	// TypeConfext is the systemd-confext provisioner (merges over /etc).
	TypeConfext ExtensionType = "confext"
)

// VersionInfo describes the systemd version detected on the guest by
// Preflight.
type VersionInfo struct {
	// Major is the parsed major systemd version, e.g. 255 for both
	// "systemd 255 (255.4-1ubuntu8)" and "systemd v255 (...)" output.
	Major int
	// Raw is the first line of `systemctl --version` output with trailing
	// whitespace trimmed.
	Raw string
}

// FindCommandArgs returns the argv for resolving a guest command:
// `command -v <cmd>`. The slice is plain argv; it is never
// passed to a shell.
func FindCommandArgs(cmd string) []string {
	return []string{"command", "-v", cmd}
}

// SystemctlVersionArgs returns the argv for reading the guest systemd
// version: `systemctl --version`.
func SystemctlVersionArgs() []string {
	return []string{"systemctl", "--version"}
}

// StatusArgs returns the argv for `<cmd> status`, i.e. `systemd-sysext
// status` or `systemd-confext status`.
func StatusArgs(cmd string) []string {
	return []string{cmd, "status"}
}

// MergeArgs returns the argv for `<cmd> merge`, i.e. `systemd-sysext merge`
// or `systemd-confext merge`.
func MergeArgs(cmd string) []string {
	return []string{cmd, "merge"}
}

// UnmergeArgs returns the argv for `<cmd> unmerge`, i.e. `systemd-sysext
// unmerge` or `systemd-confext unmerge`.
func UnmergeArgs(cmd string) []string {
	return []string{cmd, "unmerge"}
}

// EnableArgs returns the argv for enabling the boot-time activation unit:
// `systemctl enable <unit>`.
func EnableArgs(unit string) []string {
	return []string{"systemctl", "enable", unit}
}

// IsEnabledArgs returns the argv for verifying unit enablement:
// `systemctl is-enabled <unit>`.
func IsEnabledArgs(unit string) []string {
	return []string{"systemctl", "is-enabled", unit}
}

// run executes args on the guest via comm.Start and blocks until the command
// exits. It returns the command's stdout and stderr. On a non-zero exit it
// returns an error carrying the command line, the exit status, and a
// whitespace-trimmed tail of stderr.
func run(ctx context.Context, comm packersdk.Communicator, args []string) (string, string, error) {
	cmdLine := strings.Join(args, " ")
	var stdout, stderr strings.Builder
	rc := &packersdk.RemoteCmd{
		Command: cmdLine,
		Stdout:  &stdout,
		Stderr:  &stderr,
	}
	if err := comm.Start(ctx, rc); err != nil {
		return "", "", fmt.Errorf("failed to start %q: %w", cmdLine, err)
	}
	if status := rc.Wait(); status != 0 {
		return stdout.String(), stderr.String(), fmt.Errorf("%s: exit status %d: %s", cmdLine, status, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), stderr.String(), nil
}

// Preflight verifies, in order, that the guest can run the provisioner:
//
//  1. `command -v <command>` resolves the configured command; a non-zero exit
//     yields GUEST_COMMAND_NOT_FOUND. Resolution is exit-status based, so a
//     zero exit with empty stdout still counts as resolved.
//  2. `systemctl --version` parses to a major version >= 252 for sysext and
//     >= 254 for confext; a lower or unparseable version yields
//     SYSTEMD_TOO_OLD with the detected version in the message.
//  3. `id -u` reports 0; any other value yields NOT_ROOT.
//
// Preflight stops at the first failing check. On success it returns the
// detected VersionInfo. ui is reserved for progress reporting by callers;
// the checks themselves emit no UI output.
func Preflight(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, typ ExtensionType, command string) (VersionInfo, error) {
	if _, _, err := run(ctx, comm, FindCommandArgs(command)); err != nil {
		return VersionInfo{}, &Error{
			Code:   CodeGuestCommandNotFound,
			Op:     "preflight command " + command,
			Detail: err.Error(),
		}
	}

	versionOut, _, err := run(ctx, comm, SystemctlVersionArgs())
	if err != nil {
		return VersionInfo{}, &Error{
			Code:   CodeSystemdTooOld,
			Op:     "systemctl --version",
			Detail: err.Error(),
		}
	}
	info := parseVersion(versionOut)
	floor := 252
	if typ == TypeConfext {
		floor = 254
	}
	if info.Major < floor {
		return VersionInfo{}, &Error{
			Code:   CodeSystemdTooOld,
			Op:     "systemctl --version",
			Detail: fmt.Sprintf("systemd version %d (%s) is older than required %d", info.Major, info.Raw, floor),
		}
	}

	idOut, _, err := run(ctx, comm, []string{"id", "-u"})
	if err != nil {
		return VersionInfo{}, &Error{
			Code:   CodeNotRoot,
			Op:     "id -u",
			Detail: err.Error(),
		}
	}
	if strings.TrimSpace(idOut) != "0" {
		return VersionInfo{}, &Error{
			Code:   CodeNotRoot,
			Op:     "id -u",
			Detail: fmt.Sprintf("expected root uid 0, got %q", strings.TrimSpace(idOut)),
		}
	}
	return info, nil
}

// parseVersion extracts VersionInfo from `systemctl --version` output. Raw is
// the first line with surrounding whitespace trimmed. Major is the first
// integer token on that line, accepting both "systemd 255 (...)" and
// "systemd v254 (...)" formats; it is 0 when no integer token is found
// (unparseable).
func parseVersion(out string) VersionInfo {
	first := out
	if i := strings.IndexByte(first, '\n'); i >= 0 {
		first = first[:i]
	}
	info := VersionInfo{Raw: strings.TrimSpace(first)}
	for _, field := range strings.Fields(first) {
		if n, err := strconv.Atoi(strings.TrimPrefix(field, "v")); err == nil {
			info.Major = n
			break
		}
	}
	return info
}
