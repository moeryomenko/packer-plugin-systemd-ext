package extpkg

import (
	"fmt"
	"io"
	"os"
	"os/exec"
)

// PackageImage stages a directory source and packages it into a disk image
// (.raw) at dstFile. Staging is delegated to
// PackageDirectory, so srcDir, name, osRelease, override, and typ behave
// exactly as documented there: the source contents are copied under
// dstDir/usr/ (TypeSysext) or dstDir/etc/ (TypeConfext), the extension-release
// file is written by WriteReleaseFile, and file/directory modes and symlinks
// are preserved. The staged tree is then handed to the packaging tool for
// format, with arguments built as a plain slice (never a shell):
//
//   - FormatSquashfs: mksquashfs <staged> <dstFile> -noappend
//   - FormatErofs:    mkfs.erofs <dstFile> <staged>
//
// format is validated first, before any tool lookup or file I/O: an
// unsupported value returns an error mentioning "format" and creates nothing
// at dstFile. The packaging tool is resolved on PATH at call time, so a PATH
// override is honored; when it is absent the returned error names the missing
// binary ("mksquashfs" / "mkfs.erofs"). When the tool fails, the returned
// error wraps its exit status (via *exec.ExitError) and includes a tail of the
// tool's combined output.
func PackageImage(srcDir, dstFile, name string, format ImageFormat, osRelease io.Reader, override []byte, typ ExtensionType) error {
	var tool string
	switch format {
	case FormatSquashfs:
		tool = "mksquashfs"
	case FormatErofs:
		tool = "mkfs.erofs"
	default:
		return fmt.Errorf("format: unsupported image format %q", format)
	}

	if err := ValidateName(name); err != nil {
		return err
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return fmt.Errorf("source %q: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", srcDir)
	}

	staged, err := os.MkdirTemp("", "packer-image-staging-")
	if err != nil {
		return fmt.Errorf("create staging dir: %w", err)
	}
	defer func() { _ = os.RemoveAll(staged) }()
	if err := PackageDirectory(srcDir, staged, name, osRelease, typ, override); err != nil {
		return fmt.Errorf("stage source %q: %w", srcDir, err)
	}

	toolPath, err := exec.LookPath(tool)
	if err != nil {
		return fmt.Errorf("%s: %w", tool, err)
	}

	var args []string
	switch format {
	case FormatSquashfs:
		args = []string{staged, dstFile, "-noappend"}
	case FormatErofs:
		args = []string{dstFile, staged}
	}
	cmd := exec.Command(toolPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s failed: %w\n%s", tool, err, out)
	}
	return nil
}
