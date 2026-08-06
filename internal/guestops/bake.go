package guestops

import (
	"context"
	"fmt"
	"path"
	"strings"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// CodeMergeFailed reports that the guest merge command (systemd-sysext merge
// / systemd-confext merge) exited non-zero during bake validation.
const CodeMergeFailed Code = "MERGE_FAILED"

// CodeBakeFailed reports a failure in a bake-mode guest command other than
// the merge itself (unmerge, copy into the rootfs, artifact removal,
// release-file removal).
const CodeBakeFailed Code = "BAKE_FAILED"

// BakeExtension describes one placed extension for the bake sequence.
type BakeExtension struct {
	// Name is the extension name; it names the release file removed at the
	// end of the bake sequence and the temp bake directory used
	// for image-format sources.
	Name string
	// ImageFormat is "directory" for a staged tree or "raw" for a disk image
	// (a prebuilt .raw source, or a directory packaged to squashfs/erofs).
	ImageFormat string
	// Basenames are the artifact names inside the install directory that the
	// bake sequence removes after copying content: the directory
	// name for directory format, or the .raw and verity-companion basenames
	// for image format. The first basename of an image-format extension is
	// the disk image read by systemd-dissect.
	Basenames []string
}

// Bake performs the bake sequence for one placed extension:
//
//  1. status check; when the hierarchy is already merged, unmerge first with
//     a UI warning.
//  2. merge; a non-zero exit captures status output and fails with
//     MERGE_FAILED including the extension name.
//  3. unmerge.
//  4. copy the extension content into the base rootfs: `cp -a
//     <installDir>/<name>/. /` for directory format; `systemd-dissect
//     --copy-from <image> / <tmp>/`, `cp -a <tmp>/. /`, then `rm -rf <tmp>`
//     for image format.
//  5. remove the extension artifacts from the install directory.
//  6. remove the baked release file.
//
// Bake never enables any boot service. All commands are issued as
// plain argv, never through a shell.
func Bake(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, typ ExtensionType, command, installDir string, ext BakeExtension) error {
	// If status reports the hierarchy already merged, unmerge first so the
	// validation merge starts from a clean slate.
	statusOut, _, err := run(ctx, comm, StatusArgs(command))
	if err != nil {
		return bakeError(strings.Join(StatusArgs(command), " "), err)
	}
	// systemd 255 prints "STATUS: MERGED" (uppercase); match
	// case-insensitively so the already-merged path triggers.
	if strings.Contains(strings.ToLower(statusOut), "merged") {
		ui.Say(fmt.Sprintf("%s: hierarchy already merged; unmerging before merge (%s)", command, ext.Name))
		if _, _, err := run(ctx, comm, UnmergeArgs(command)); err != nil {
			return bakeError(strings.Join(UnmergeArgs(command), " "), err)
		}
	}

	// Merge validates the extension. For a prebuilt image it also verifies
	// the image's release file, surfacing a missing or mismatched release file
	// as a merge failure. On failure, capture status
	// output and report MERGE_FAILED with the extension name and the merge
	// command line.
	if _, _, err := run(ctx, comm, MergeArgs(command)); err != nil {
		captured, _, _ := run(ctx, comm, StatusArgs(command))
		return &Error{
			Code: CodeMergeFailed,
			Op:   strings.Join(MergeArgs(command), " "),
			Detail: fmt.Sprintf("extension %s: %v; status output: %s",
				ext.Name, err, strings.TrimSpace(captured)),
		}
	}

	if _, _, err := run(ctx, comm, UnmergeArgs(command)); err != nil {
		return bakeError(strings.Join(UnmergeArgs(command), " "), err)
	}

	// Copy the extension content into the base rootfs.
	if err := copyIntoRootfs(ctx, comm, installDir, ext); err != nil {
		return err
	}

	// Remove the extension artifacts from the install directory.
	if err := removeArtifacts(ctx, comm, installDir, ext.Basenames); err != nil {
		return err
	}

	// Remove the baked release file.
	releasePath := releaseFilePath(typ, ext.Name)
	if _, _, err := run(ctx, comm, []string{"rm", "-f", releasePath}); err != nil {
		return bakeError("rm -f "+releasePath, err)
	}
	return nil
}

// copyIntoRootfs copies the baked extension content into the base rootfs.
// For directory format the staged tree (which carries the usr/
// or etc/ prefix) is copied with `cp -a <installDir>/<name>/. /`. For image
// format the content is read out of the disk image with systemd-dissect into
// a private temp dir, copied with `cp -a <tmp>/. /`, then the temp dir is
// removed; systemd-dissect honors any verity companions present.
func copyIntoRootfs(ctx context.Context, comm packersdk.Communicator, installDir string, ext BakeExtension) error {
	if ext.ImageFormat == "directory" {
		dirPath := path.Join(installDir, ext.Name)
		if _, _, err := run(ctx, comm, []string{"cp", "-a", dirPath + "/.", "/"}); err != nil {
			return bakeError("cp -a "+dirPath+"/. /", err)
		}
		return nil
	}
	imagePath := path.Join(installDir, ext.Basenames[0])
	tmp := "/var/tmp/packer-bake-" + ext.Name
	if _, _, err := run(ctx, comm, []string{"systemd-dissect", "--copy-from", imagePath, "/", tmp + "/"}); err != nil {
		return bakeError("systemd-dissect --copy-from "+imagePath+" / "+tmp+"/", err)
	}
	if _, _, err := run(ctx, comm, []string{"cp", "-a", tmp + "/.", "/"}); err != nil {
		return bakeError("cp -a "+tmp+"/. /", err)
	}
	if _, _, err := run(ctx, comm, []string{"rm", "-rf", tmp}); err != nil {
		return bakeError("rm -rf "+tmp, err)
	}
	return nil
}

// removeArtifacts removes every artifact of the extension from the install
// directory with a single `rm -rf`. rm -rf is safe on already-absent
// paths, which keeps the bake cleanup idempotent.
func removeArtifacts(ctx context.Context, comm packersdk.Communicator, installDir string, basenames []string) error {
	args := []string{"rm", "-rf"}
	for _, b := range basenames {
		args = append(args, path.Join(installDir, b))
	}
	if _, _, err := run(ctx, comm, args); err != nil {
		return bakeError(strings.Join(args, " "), err)
	}
	return nil
}

// releaseFilePath returns the baked release-file path for the extension type:
// /usr/lib/extension-release.d/extension-release.<name> for sysext and
// /etc/extension-release.d/extension-release.<name> for confext.
func releaseFilePath(typ ExtensionType, name string) string {
	dir := "/usr/lib/extension-release.d"
	if typ == TypeConfext {
		dir = "/etc/extension-release.d"
	}
	return path.Join(dir, "extension-release."+name)
}

// bakeError wraps a failed bake-mode guest command into a typed Error with
// code BAKE_FAILED.
func bakeError(op string, err error) error {
	return &Error{Code: CodeBakeFailed, Op: op, Detail: err.Error()}
}
