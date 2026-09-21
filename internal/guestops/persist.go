package guestops

import (
	"context"
	"fmt"
	"strings"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// CodeServiceEnableFailed reports that the persist-mode boot service could
// not be verified as enabled: either `systemctl enable <unit>` or the
// `systemctl is-enabled <unit>` verification exited non-zero. The message
// includes the failing command line and the exit code.
const CodeServiceEnableFailed Code = "SERVICE_ENABLE_FAILED"

// PersistExtension describes one placed extension for the persist sequence.
// Placement itself is shared with bake; persist never
// removes artifacts, so the struct carries only the metadata the persist
// commands need.
type PersistExtension struct {
	// Name is the extension name, used in the already-merged UI warning.
	Name string
}

// bootServiceUnit returns the systemd unit that re-applies the extensions at
// boot: systemd-sysext.service for sysext and systemd-confext.service for
// confext. Enabling this unit is what makes persist mode survive a reboot;
// without it the extensions are never merged at boot.
func bootServiceUnit(typ ExtensionType) string {
	if typ == TypeConfext {
		return "systemd-confext.service"
	}
	return "systemd-sysext.service"
}

// Persist performs the persist sequence for one placed extension:
//
//  1. When mergeDuringBuild is true: `status` checks the merge state; if it
//     reports the hierarchy already merged, `unmerge` first with a UI warning,
//     then `merge`. A failed status/unmerge/merge command is reported with
//     code MERGE_FAILED.
//  2. Always: `systemctl enable <boot service>` then verify with `systemctl
//     is-enabled <boot service>`; a non-zero verification exit fails with
//     SERVICE_ENABLE_FAILED.
//
// When mergeDuringBuild is false no status/merge/unmerge commands are issued.
// When enableOnBoot is false, persist leaves activation to the caller (for
// example, a delayed first-boot merge service) and does not touch the stock
// systemd extension service.
// Artifacts remain in the install directory — persist never issues rm.
// Running Persist twice yields the same command
// stream each run: exactly one merge per run when mergeDuringBuild is true,
// at most one unmerge (only when status reports merged), and exactly one
// enable + is-enabled per run even when the unit is already enabled —
// enablement is always issued. All commands are plain argv,
// never through a shell.
func Persist(
	ctx context.Context,
	ui packersdk.Ui,
	comm packersdk.Communicator,
	typ ExtensionType,
	command string,
	ext PersistExtension,
	mergeDuringBuild bool,
	enableOnBoot bool,
) error {
	if mergeDuringBuild {
		statusOut, _, err := run(ctx, comm, StatusArgs(command))
		if err != nil {
			return persistMergeError(strings.Join(StatusArgs(command), " "), err)
		}
		// When the hierarchy is already merged, unmerge first so the merge
		// starts from a clean slate.
		// systemd 255 prints "STATUS: MERGED" (uppercase); match
		// case-insensitively so the already-merged path triggers.
		if strings.Contains(strings.ToLower(statusOut), "merged") {
			ui.Say(fmt.Sprintf("%s: hierarchy already merged; unmerging before merge (%s)", command, ext.Name))
			if _, _, err := run(ctx, comm, UnmergeArgs(command)); err != nil {
				return persistMergeError(strings.Join(UnmergeArgs(command), " "), err)
			}
		}
		if _, _, err := run(ctx, comm, MergeArgs(command)); err != nil {
			return persistMergeError(strings.Join(MergeArgs(command), " "), err)
		}
	}

	if !enableOnBoot {
		return nil
	}

	// Always enable the boot service — even when it is already enabled — and
	// verify with is-enabled. The verification is exit-status based
	// (mirroring the command -v check); any non-zero exit fails the build.
	unit := bootServiceUnit(typ)
	enableLine := strings.Join(EnableArgs(unit), " ")
	if _, _, err := run(ctx, comm, EnableArgs(unit)); err != nil {
		return &Error{Code: CodeServiceEnableFailed, Op: enableLine, Detail: err.Error()}
	}
	isEnabledLine := strings.Join(IsEnabledArgs(unit), " ")
	if _, _, err := run(ctx, comm, IsEnabledArgs(unit)); err != nil {
		return &Error{Code: CodeServiceEnableFailed, Op: isEnabledLine, Detail: err.Error()}
	}
	return nil
}

// persistMergeError wraps a failed persist-mode merge-state guest command
// (status, unmerge, merge) into a typed Error with code MERGE_FAILED — the
// same code bake uses for a failed merge.
func persistMergeError(op string, err error) error {
	return &Error{Code: CodeMergeFailed, Op: op, Detail: err.Error()}
}
