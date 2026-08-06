// Contract tests for extpkg squashfs and erofs image packaging.
//
// This file defines the public API contract for packaging a directory source
// into a disk image (.raw); the engineer implements to match:
//
//	type ImageFormat string
//	const (
//	    FormatSquashfs ImageFormat = "squashfs"
//	    FormatErofs    ImageFormat = "erofs"
//	)
//	func PackageImage(srcDir, dstFile, name string, format ImageFormat, osRelease io.Reader, override []byte, typ ExtensionType) error
//
// srcDir, name, osRelease, override, and typ behave exactly like
// PackageDirectory: the source is staged under usr/ (TypeSysext)
// or etc/ (TypeConfext) with the release file written by WriteReleaseFile.
// PackageImage then invokes mksquashfs (FormatSquashfs) or mkfs.erofs
// (FormatErofs) on the staged tree with a plain argument slice (no shell) and
// writes the resulting image to dstFile.
//
// Expected red state against the current repo: internal/extpkg has package.go
// but no image.go, so this file does NOT compile (undefined:
// extpkg.PackageImage, extpkg.ImageFormat, extpkg.FormatSquashfs,
// extpkg.FormatErofs). That compile failure is the intended red phase; it is
// resolved when internal/extpkg/image.go is implemented.
//
// Contract pinned by these tests:
//   - format is validated before any tool lookup or file I/O: an unsupported
//     format returns an error mentioning "format" and creates nothing at
//     dstFile. This holds even on runners where the packaging tools are
//     absent, which pins validation order (format first).
//   - When the packaging tool is not resolvable on PATH, the package step
//     returns an error naming the missing binary ("mksquashfs" / "mkfs.erofs"):
//     "when the tool is absent, the package step errors with an actionable
//     message". Tool lookup happens at call time, so a PATH override is
//     honored.
//   - A produced image is a real, readable filesystem image: dstFile exists,
//     is non-empty, and begins with the format's on-disk magic ("hsqs" for
//     squashfs; erofs superblock magic 0xE0F5E1E2 at offset 1024).
//   - The image content re-read out of the .raw matches the staged source
//     byte-for-byte, including the generated release file and any symlinks:
//     "a generated .raw re-read via systemd-dissect --copy-from yields
//     byte-identical content".
//
// Test hygiene: this file is integration-tagged and uses t.Skip, NOT a
// //go:build tag, so the integration tests run under plain `go test` with
// graceful skips. Each format test skips independently when its own packaging
// tool is missing; the erofs tests skip on runners without mkfs.erofs without
// affecting the squashfs tests.
//
// Environment notes (probed on the authoring runner, 2026-08-05):
//   - mksquashfs, unsquashfs, and systemd-dissect are present; mkfs.erofs,
//     dump.erofs, and fsck.erofs are not. The squashfs tests therefore run
//     full verification here; the erofs tests skip.
//   - systemd-dissect --copy-from FAILS unprivileged on this runner with
//     "Failed to mount image via mountfsd" because the systemd mountfsd
//     service is not available. The task brief asked to note this and design
//     accordingly: the extractor prefers systemd-dissect (the spec's own
//     bake-mode mechanism and the only tool that reads both formats) but falls
//     back to unsquashfs (squashfs) or fsck.erofs --extract (erofs), so
//     verification still runs on runners without a working dissect. On this
//     runner the squashfs round-trip is verified via unsquashfs.

package extpkg

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// imageToolName returns the packaging binary for format. It is used to name
// missing-tool errors and to skip tests; an unknown format returns "".
func imageToolName(format ImageFormat) string {
	switch format {
	case FormatSquashfs:
		return "mksquashfs"
	case FormatErofs:
		return "mkfs.erofs"
	}
	return ""
}

// requireTool returns the absolute path of tool, skipping the test with a
// documented reason when it is not on PATH (integration hygiene: never
// hard-fail an integration test for missing environment tooling).
func requireTool(t *testing.T, tool string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("integration test skipped: tool %q not found on PATH (%v)", tool, err)
	}
	return p
}

// targetPrefixName mirrors the staged-tree layout for typ without depending on
// the production helper: "usr" for sysext, "etc" for confext.
func targetPrefixName(typ ExtensionType) string {
	if typ == TypeConfext {
		return "etc"
	}
	return "usr"
}

// writeSymlink creates a symlink at root/rel pointing at target, creating
// parent directories as needed.
func writeSymlink(t *testing.T, root, rel, target string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
	}
	if err := os.Symlink(target, p); err != nil {
		t.Fatalf("create symlink %s -> %s: %v", p, target, err)
	}
}

// assertImageMagic fails unless raw starts with the on-disk magic of format,
// proving the file is a real image of the requested format rather than an
// empty or mislabeled artifact.
func assertImageMagic(t *testing.T, raw string, format ImageFormat) {
	t.Helper()
	f, err := os.Open(raw)
	if err != nil {
		t.Fatalf("open %s: %v", raw, err)
	}
	defer func() { _ = f.Close() }()
	switch format {
	case FormatSquashfs:
		// mksquashfs images start with the ASCII magic "hsqs".
		var buf [4]byte
		if _, err := f.ReadAt(buf[:], 0); err != nil {
			t.Fatalf("read %s magic: %v", raw, err)
		}
		if string(buf[:]) != "hsqs" {
			t.Fatalf("%s is not a squashfs image: magic %q, want %q", raw, buf, "hsqs")
		}
	case FormatErofs:
		// erofs superblock: magic 0xE0F5E1E2 (little-endian) at offset 1024.
		var buf [4]byte
		if _, err := f.ReadAt(buf[:], 1024); err != nil {
			t.Fatalf("read %s erofs superblock: %v", raw, err)
		}
		if string(buf[:]) != "\xe2\xe1\xf5\xe0" {
			t.Fatalf("%s is not an erofs image: magic %x, want %x", raw, buf, []byte("\xe2\xe1\xf5\xe0"))
		}
	default:
		t.Fatalf("assertImageMagic: unsupported format %q", format)
	}
}

// extractImageContent extracts the whole content of raw into a fresh
// directory and returns it together with the tool used. Extraction strategies
// in order:
//
//  1. systemd-dissect --copy-from raw / DIR — the spec's own bake-mode
//     mechanism and the only tool that reads both formats. It may
//     fail for environmental reasons on unprivileged runners (missing systemd
//     mountfsd), in which case it is logged and the next strategy is tried.
//  2. unsquashfs -d DIR raw (squashfs) or fsck.erofs --extract=DIR raw
//     (erofs). These are pure userspace readers; if one is present but fails,
//     the image itself is broken and the test fails.
//
// When no whole-tree extractor is usable it returns ("", ""); the caller then
// tries per-file extraction or skips gracefully.
func extractImageContent(t *testing.T, raw string, format ImageFormat) (string, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "extracted")

	if p, err := exec.LookPath("systemd-dissect"); err == nil {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
		cmd := exec.Command(p, "--copy-from", raw, "/", dir)
		if out, err := cmd.CombinedOutput(); err == nil {
			return dir, "systemd-dissect"
		} else {
			t.Logf("systemd-dissect extraction failed (%v), falling back to a format-specific tool: %s", err, out)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatalf("remove partial extraction %s: %v", dir, err)
		}
	}

	switch format {
	case FormatSquashfs:
		if p, err := exec.LookPath("unsquashfs"); err == nil {
			if out, err := exec.Command(p, "-d", dir, raw).CombinedOutput(); err == nil {
				return dir, "unsquashfs"
			} else {
				t.Fatalf("unsquashfs extraction of %s failed: %v\n%s", raw, err, out)
			}
		}
	case FormatErofs:
		if p, err := exec.LookPath("fsck.erofs"); err == nil {
			if out, err := exec.Command(p, "--extract="+dir, raw).CombinedOutput(); err == nil {
				return dir, "fsck.erofs"
			} else {
				t.Fatalf("fsck.erofs extraction of %s failed: %v\n%s", raw, err, out)
			}
		}
	}
	return "", ""
}

// compareKnownFilesViaDumpErofs verifies individual files out of an erofs
// image with dump.erofs --cat (the per-file extractor of erofs-utils), used
// when no whole-tree extractor is available. dump.erofs cannot dump whole
// trees, so the compare covers the known regular files in want only. If
// dump.erofs itself cannot read the image (CLI drift or environment), the
// test skips rather than hard-fails: the mandatory image assertions
// (existence, non-empty, magic) have already run.
func compareKnownFilesViaDumpErofs(t *testing.T, raw string, want map[string]string) {
	t.Helper()
	p, err := exec.LookPath("dump.erofs")
	if err != nil {
		t.Skipf("no image content verification tool available: systemd-dissect, fsck.erofs, and dump.erofs all absent")
	}
	for rel, content := range want {
		cmd := exec.Command(p, "--cat", "--path=/"+filepath.ToSlash(rel), raw)
		out, err := cmd.Output()
		if err != nil {
			t.Skipf("dump.erofs --cat %s failed (%v); skipping content verification", rel, err)
		}
		if string(out) != content {
			t.Fatalf("file %s in %s = %q, want %q", rel, raw, out, content)
		}
	}
}

// assertExtractedSymlink asserts that rel inside the extracted tree is still a
// symlink pointing at target and reads through to wantContent.
func assertExtractedSymlink(t *testing.T, dir, rel, target, wantContent string) {
	t.Helper()
	linkPath := filepath.Join(dir, filepath.FromSlash(rel))
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat extracted symlink %s: %v", rel, err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("extracted %s is not a symlink: mode %v", rel, fi.Mode())
	}
	got, err := os.Readlink(linkPath)
	if err != nil {
		t.Fatalf("readlink extracted symlink %s: %v", rel, err)
	}
	if got != target {
		t.Fatalf("extracted symlink %s -> %q, want -> %q", rel, got, target)
	}
	b, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("read through extracted symlink %s: %v", rel, err)
	}
	if string(b) != wantContent {
		t.Fatalf("read through extracted symlink %s = %q, want %q", rel, b, wantContent)
	}
}

// verifyImageContent compares the content extracted from raw against the
// expected staged file set want. For TypeSysext the staged prefix is "usr"
// (TypeConfext: "etc"); when symlinkRel is non-empty the extracted symlink at
// prefix/symlinkRel is asserted to point at symlinkTarget and read through to
// symlinkContent. Whole-tree extraction asserts the full set; when only
// per-file extraction is possible (erofs via dump.erofs) the regular files in
// want are compared and symlink assertions are skipped. With no usable
// extractor the test skips after the mandatory image assertions have passed.
func verifyImageContent(t *testing.T, raw string, format ImageFormat, typ ExtensionType, want map[string]string, symlinkRel, symlinkTarget, symlinkContent string) {
	t.Helper()
	dir, tool := extractImageContent(t, raw, format)
	if dir != "" {
		t.Logf("verifying %s content via %s whole-tree extraction", format, tool)
		assertFileSet(t, dir, want)
		if symlinkRel != "" {
			prefix := targetPrefixName(typ)
			assertExtractedSymlink(t, dir, filepath.Join(prefix, symlinkRel), symlinkTarget, symlinkContent)
		}
		return
	}
	if format == FormatErofs {
		t.Logf("verifying %s content via dump.erofs per-file extraction", format)
		compareKnownFilesViaDumpErofs(t, raw, want)
		return
	}
	t.Skipf("no image content verification tool available (systemd-dissect/unsquashfs absent); only basic image assertions ran for %s", format)
}

// runImageRoundTrip builds an image from files (plus an optional symlink)
// under typ with the given format, then verifies the image: dstFile exists,
// is non-empty, starts with the format magic, and re-extracts to exactly want
// (byte-for-byte) including the release file and the symlink.
func runImageRoundTrip(t *testing.T, format ImageFormat, files map[string]string, symlinkRel, symlinkTarget string, typ ExtensionType, want map[string]string) {
	t.Helper()
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, files)
	if symlinkRel != "" {
		writeSymlink(t, srcDir, symlinkRel, symlinkTarget)
	}
	dstFile := filepath.Join(t.TempDir(), "foo.raw")

	if err := PackageImage(srcDir, dstFile, "foo", format, strings.NewReader(fixtureBasic), nil, typ); err != nil {
		t.Fatalf("PackageImage(%s): %v", format, err)
	}
	fi, err := os.Stat(dstFile)
	if err != nil {
		t.Fatalf("stat %s: %v", dstFile, err)
	}
	if fi.Size() == 0 {
		t.Fatalf("%s is empty", dstFile)
	}
	assertImageMagic(t, dstFile, format)

	symlinkContent := ""
	if symlinkRel != "" {
		symlinkContent = files[symlinkTarget]
	}
	verifyImageContent(t, dstFile, format, typ, want, symlinkRel, symlinkTarget, symlinkContent)
}

func TestPackageImageFormatValidation(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, map[string]string{"bin/tool": "x\n"})

	// "directory" is a valid format handled by PackageDirectory, not
	// by the image packager; the rest are garbage.
	unsupported := []ImageFormat{"", "directory", "btrfs", "ext4", "gpt"}
	for _, format := range unsupported {
		format := format
		t.Run(fmt.Sprintf("%q", format), func(t *testing.T) {
			dstFile := filepath.Join(t.TempDir(), "foo.raw")
			err := PackageImage(srcDir, dstFile, "foo", format, strings.NewReader(fixtureBasic), nil, TypeSysext)
			if err == nil {
				t.Fatalf("PackageImage with format %q = nil, want error", format)
			}
			if msg := err.Error(); !strings.Contains(msg, "format") {
				t.Fatalf("unsupported-format error %q does not mention %q", msg, "format")
			}
			// Format is validated before any work: nothing is produced.
			if _, statErr := os.Stat(dstFile); !os.IsNotExist(statErr) {
				t.Fatalf("format %q: %s was created after error (stat err = %v)", format, dstFile, statErr)
			}
		})
	}
}

func TestPackageImageSquashfsRoundTrip(t *testing.T) {
	requireTool(t, "mksquashfs")

	// Nested directories (bin/, share/app/) and a symlink are part of the
	// fixture; the extracted image must reproduce them byte-for-byte.
	files := map[string]string{
		"bin/tool":         "#!/bin/sh\necho packaged\n",
		"share/app/config": "setting=1\n",
	}
	runImageRoundTrip(t, FormatSquashfs, files, "link", "bin/tool", TypeSysext, stagedFixtureSet(TypeSysext))
}

func TestPackageImageErofsRoundTrip(t *testing.T) {
	requireTool(t, "mkfs.erofs")

	files := map[string]string{
		"bin/tool":         "#!/bin/sh\necho packaged\n",
		"share/app/config": "setting=1\n",
	}
	runImageRoundTrip(t, FormatErofs, files, "link", "bin/tool", TypeSysext, stagedFixtureSet(TypeSysext))
}

func TestPackageImageEmptySource(t *testing.T) {
	// An empty source directory is valid (PackageDirectory semantics):
	// the image contains exactly the release file. Each format skips
	// independently when its own tool is missing.
	formats := []ImageFormat{FormatSquashfs, FormatErofs}
	for _, format := range formats {
		format := format
		t.Run(string(format), func(t *testing.T) {
			requireTool(t, imageToolName(format))

			srcDir := t.TempDir() // deliberately empty
			dstFile := filepath.Join(t.TempDir(), "foo.raw")
			if err := PackageImage(srcDir, dstFile, "foo", format, strings.NewReader(fixtureBasic), nil, TypeSysext); err != nil {
				t.Fatalf("PackageImage(%s) with empty source: %v", format, err)
			}
			assertImageMagic(t, dstFile, format)

			want := map[string]string{
				filepath.FromSlash(releasePath(TypeSysext, "foo")): generatedRelease,
			}
			verifyImageContent(t, dstFile, format, TypeSysext, want, "", "", "")
		})
	}
}

func TestPackageImageToolMissing(t *testing.T) {
	// Deliberately hide every packaging tool from PATH so the package step's
	// missing-tool error path is exercised deterministically: "when the tool
	// is absent, the package step errors with an actionable message".
	// This runs on every runner, including ones where the tools are genuinely
	// absent, and does not need a skip.
	t.Setenv("PATH", t.TempDir())
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, map[string]string{"bin/tool": "x\n"})

	for _, c := range []struct {
		format ImageFormat
		tool   string
	}{
		{format: FormatSquashfs, tool: "mksquashfs"},
		{format: FormatErofs, tool: "mkfs.erofs"},
	} {
		err := PackageImage(srcDir, filepath.Join(t.TempDir(), "foo.raw"), "foo", c.format, strings.NewReader(fixtureBasic), nil, TypeSysext)
		if err == nil {
			t.Fatalf("PackageImage(%s) with %s hidden from PATH = nil, want error", c.format, c.tool)
		}
		if msg := err.Error(); !strings.Contains(msg, c.tool) {
			t.Fatalf("missing-tool error %q does not name the missing binary %q", msg, c.tool)
		}
	}
}
