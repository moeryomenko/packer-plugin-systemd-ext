// Contract tests for extpkg directory packaging for systemd-sysext and
// systemd-confext extension images.
//
// This file defines the public API contract for packaging a directory source
// into a staged extension tree; the engineer implements to match:
//
//	func PackageDirectory(srcDir, dstDir, name string, osRelease io.Reader, typ ExtensionType, override []byte) error
//
// srcDir is the local extension payload, dstDir is the root of the staged
// tree, osRelease/override feed the release file exactly like
// WriteReleaseFile, and typ selects the target hierarchy.
//
// Expected red state against the current repo: internal/extpkg has name.go +
// release.go but no packaging functions, so this file does NOT compile
// (undefined: extpkg.PackageDirectory). That compile failure is the intended
// red phase; it is resolved when internal/extpkg/package.go is implemented.
//
// Contract pinned by these tests:
//   - name is validated first (ValidateName); an invalid name fails before any
//     staging happens (no usr/ or etc/ directory appears in dstDir) and the
//     error mentions "name".
//   - srcDir must be a directory; passing a regular file (e.g. a prebuilt
//     .raw) returns an error mentioning "directory" (wrong source type).
//   - For TypeSysext the source contents are copied under dstDir/usr/; for
//     TypeConfext under dstDir/etc/. Nested directories are preserved.
//   - File modes (permissions plus setuid/setgid/sticky bits) and directory
//     modes are preserved through staging. The setuid assertion is skipped
//     (t.Logf) when the temp filesystem cannot store setuid bits at all
//     (nosuid mounts such as a tmpfs /tmp); the other mode assertions always
//     run.
//   - Symlinks are copied as symlinks (not followed/dereferenced) and still
//     resolve inside the staged tree.
//   - The release file is written at
//     dstDir/usr/lib/extension-release.d/extension-release.<name>
//     (TypeSysext) or dstDir/etc/extension-release.d/extension-release.<name>
//     (TypeConfext), generated from osRelease when override is nil and written
//     byte-for-byte when override is non-nil (WriteReleaseFile semantics).
//   - An empty source directory is valid and stages just the release file.
//   - No other content is produced: the staged file set is exactly the copied
//     sources plus the release file.

package extpkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// modeMask selects the mode bits a package step is required to preserve:
// permissions plus setuid/setgid/sticky (os.FileMode.Perm() alone would hide
// the setuid bit that TestPackageDirectoryPreservesModes pins).
const modeMask = os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky

// sourceFixture is the payload used by the layout tests. Nested paths
// exercise directory preservation; the exact bytes pin content copying.
var sourceFixture = map[string]string{
	"bin/tool":         "#!/bin/sh\necho packaged\n",
	"share/app/config": "setting=1\n",
}

// generatedRelease is the exact content WriteReleaseFile must produce for the
// shared fixtureBasic (defined in release_test.go): ID then VERSION_ID, each
// on its own line, trailing newline, no other fields.
const generatedRelease = "ID=ubuntu\nVERSION_ID=24.04\n"

// stagedFixtureSet returns the staged file set (rel path -> content) the
// layout tests expect for the given type: every sourceFixture entry under
// "usr" (TypeSysext) or "etc" (TypeConfext) plus the generated release file
// at that type's release path.
func stagedFixtureSet(typ ExtensionType) map[string]string {
	prefix := "usr"
	if typ == TypeConfext {
		prefix = "etc"
	}
	want := make(map[string]string, len(sourceFixture)+1)
	for rel, content := range sourceFixture {
		want[filepath.Join(prefix, filepath.FromSlash(rel))] = content
	}
	want[filepath.FromSlash(releasePath(typ, "foo"))] = generatedRelease
	return want
}

// writeSourceTree creates files with the given content under root, creating
// parent directories as needed.
func writeSourceTree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", filepath.Dir(p), err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
}

// mustChmod sets the exact mode bits of path, bypassing umask so fixtures are
// deterministic.
func mustChmod(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Chmod(path, mode); err != nil {
		t.Fatalf("chmod %s %o: %v", path, mode, err)
	}
}

// walkFiles returns rel path -> content for every regular file under root
// (symlinks and directories are skipped; symlinks are asserted separately).
func walkFiles(t *testing.T, root string) map[string]string {
	t.Helper()
	files := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || d.Type()&os.ModeSymlink != 0 || !d.Type().IsRegular() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	return files
}

// assertFileSet fails the test unless the regular files under root are exactly
// the want set: no missing files, no extra files, identical content.
func assertFileSet(t *testing.T, root string, want map[string]string) {
	t.Helper()
	got := walkFiles(t, root)
	if len(got) != len(want) {
		t.Fatalf("file set under %s = %v, want %v", root, got, want)
	}
	for rel, content := range want {
		if got[rel] != content {
			t.Fatalf("file %s under %s = %q, want %q", rel, root, got[rel], content)
		}
	}
}

func TestPackageDirectorySysextLayout(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, sourceFixture)
	dstDir := t.TempDir()

	if err := PackageDirectory(srcDir, dstDir, "foo", strings.NewReader(fixtureBasic), TypeSysext, nil); err != nil {
		t.Fatalf("PackageDirectory: %v", err)
	}

	// releasePath(TypeSysext, "foo") is usr/lib/extension-release.d/extension-release.foo;
	// stagedFixtureSet carries the fixture's usr/ prefix plus that file.
	assertFileSet(t, dstDir, stagedFixtureSet(TypeSysext))
}

func TestPackageDirectoryConfextLayout(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, sourceFixture)
	dstDir := t.TempDir()

	if err := PackageDirectory(srcDir, dstDir, "foo", strings.NewReader(fixtureBasic), TypeConfext, nil); err != nil {
		t.Fatalf("PackageDirectory: %v", err)
	}

	// The confext tree is the same payload under etc/; the release file moves
	// to etc/extension-release.d/extension-release.foo.
	assertFileSet(t, dstDir, stagedFixtureSet(TypeConfext))
}

func TestPackageDirectoryEmptySource(t *testing.T) {
	srcDir := t.TempDir() // deliberately empty
	dstDir := t.TempDir()

	if err := PackageDirectory(srcDir, dstDir, "foo", strings.NewReader(fixtureBasic), TypeSysext, nil); err != nil {
		t.Fatalf("PackageDirectory with empty source: %v", err)
	}

	// An empty source is valid: the staged tree contains exactly the release
	// file (usr/ exists only as the parent of the extension-release.d path).
	want := map[string]string{
		filepath.FromSlash(releasePath(TypeSysext, "foo")): generatedRelease,
	}
	assertFileSet(t, dstDir, want)
}

// setuidSupported probes whether the temp filesystem can actually store the
// setuid bit: on nosuid mounts (e.g. a tmpfs /tmp) the kernel silently strips
// S_ISUID on chmod, so asserting setuid preservation would fail for reasons
// unrelated to the packaging code. The probe creates a file in a t.TempDir()
// (the same filesystem the fixtures use), chmods it with os.ModeSetuid|0o755
// (the Go representation of a 04755 executable; os.ModeSetuid is the 1<<23
// bit, not the low 0o4000 octal), and checks the bit survived. The
// mode-preservation test skips only the setuid assertion when the platform
// cannot represent it.
func setuidSupported(t *testing.T) bool {
	t.Helper()
	probe := filepath.Join(t.TempDir(), "probe")
	if err := os.WriteFile(probe, []byte("x"), 0o644); err != nil {
		t.Fatalf("write setuid probe: %v", err)
	}
	if err := os.Chmod(probe, os.ModeSetuid|0o755); err != nil {
		return false
	}
	fi, err := os.Stat(probe)
	return err == nil && fi.Mode()&os.ModeSetuid != 0
}

func TestPackageDirectoryPreservesModes(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, map[string]string{
		"exec":          "#!/bin/sh\n",
		"plain":         "data\n",
		"suid":          "setuid\n",
		"locked/inside": "secret\n",
	})
	// os.Chmod after creation so fixtures are exact regardless of umask.
	mustChmod(t, filepath.Join(srcDir, "exec"), 0o755)
	mustChmod(t, filepath.Join(srcDir, "plain"), 0o644)
	mustChmod(t, filepath.Join(srcDir, "suid"), os.ModeSetuid|0o755)
	mustChmod(t, filepath.Join(srcDir, "locked"), 0o700)

	dstDir := t.TempDir()
	if err := PackageDirectory(srcDir, dstDir, "foo", strings.NewReader(fixtureBasic), TypeSysext, nil); err != nil {
		t.Fatalf("PackageDirectory: %v", err)
	}

	checks := []struct {
		rel   string
		isDir bool
		want  os.FileMode // mode bits (perm + setuid/setgid/sticky)
	}{
		{rel: "usr/exec", want: 0o755},
		{rel: "usr/plain", want: 0o644},
		{rel: "usr/suid", want: os.ModeSetuid | 0o755}, // 04755: setuid bit survives
		{rel: "usr/locked", isDir: true, want: 0o700},
		{rel: "usr/locked/inside", want: 0o644},
	}
	for _, c := range checks {
		if c.want&os.ModeSetuid != 0 && !setuidSupported(t) {
			t.Logf("skipping setuid assertion for %s: temp filesystem cannot store setuid bits (nosuid mount)", c.rel)
			continue
		}
		fi, err := os.Stat(filepath.Join(dstDir, filepath.FromSlash(c.rel)))
		if err != nil {
			t.Fatalf("stat staged %s: %v", c.rel, err)
		}
		if fi.IsDir() != c.isDir {
			t.Fatalf("staged %s IsDir = %v, want %v", c.rel, fi.IsDir(), c.isDir)
		}
		if got := fi.Mode() & modeMask; got != c.want {
			t.Fatalf("mode of staged %s = %o, want %o", c.rel, got, c.want)
		}
	}
}

func TestPackageDirectoryInvalidName(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, map[string]string{"bin/tool": "x\n"})

	for _, name := range []string{"", "a/b", "..", strings.Repeat("a", 256)} {
		name := name
		t.Run(name, func(t *testing.T) {
			dstDir := t.TempDir()
			err := PackageDirectory(srcDir, dstDir, name, strings.NewReader(fixtureBasic), TypeSysext, nil)
			if err == nil {
				t.Fatalf("PackageDirectory with name %q = nil, want error", name)
			}
			if msg := err.Error(); !strings.Contains(msg, "name") {
				t.Fatalf("invalid-name error %q does not mention %q", msg, "name")
			}
			// Name is validated before packaging: nothing is staged.
			for _, p := range []string{"usr", "etc"} {
				if _, statErr := os.Stat(filepath.Join(dstDir, p)); !os.IsNotExist(statErr) {
					t.Fatalf("name %q: %s/ exists in dstDir after error (stat err = %v)", name, p, statErr)
				}
			}
		})
	}
}

func TestPackageDirectoryRawSourceError(t *testing.T) {
	srcDir := t.TempDir()
	raw := filepath.Join(srcDir, "foo.raw")
	if err := os.WriteFile(raw, []byte("raw bytes"), 0o644); err != nil {
		t.Fatalf("write fixture %s: %v", raw, err)
	}
	dstDir := t.TempDir()

	err := PackageDirectory(raw, dstDir, "foo", strings.NewReader(fixtureBasic), TypeSysext, nil)
	if err == nil {
		t.Fatal("PackageDirectory with a .raw file as srcDir = nil, want error (wrong source type)")
	}
	if msg := err.Error(); !strings.Contains(msg, "directory") {
		t.Fatalf("raw-source error %q does not mention %q", msg, "directory")
	}
}

func TestPackageDirectorySymlinks(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, map[string]string{"target": "data\n"})
	if err := os.Symlink("target", filepath.Join(srcDir, "link")); err != nil {
		t.Fatalf("create symlink fixture: %v", err)
	}
	dstDir := t.TempDir()
	if err := PackageDirectory(srcDir, dstDir, "foo", strings.NewReader(fixtureBasic), TypeSysext, nil); err != nil {
		t.Fatalf("PackageDirectory: %v", err)
	}

	// The link is staged as a symlink (Lstat), resolves to the staged target,
	// and reads through to the target's content.
	linkPath := filepath.Join(dstDir, "usr", "link")
	fi, err := os.Lstat(linkPath)
	if err != nil {
		t.Fatalf("lstat staged link: %v", err)
	}
	if fi.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("staged link is not a symlink: mode %v", fi.Mode())
	}
	resolved, err := filepath.EvalSymlinks(linkPath)
	if err != nil {
		t.Fatalf("EvalSymlinks(%s): %v", linkPath, err)
	}
	if want := filepath.Join(dstDir, "usr", "target"); resolved != want {
		t.Fatalf("staged link resolves to %s, want %s", resolved, want)
	}
	b, err := os.ReadFile(linkPath)
	if err != nil {
		t.Fatalf("read through staged link: %v", err)
	}
	if string(b) != "data\n" {
		t.Fatalf("read through staged link = %q, want %q", b, "data\n")
	}
}

func TestPackageDirectoryOverrideVerbatim(t *testing.T) {
	srcDir := t.TempDir()
	writeSourceTree(t, srcDir, map[string]string{"bin/tool": "x\n"})
	override := []byte("CUSTOM\nRELEASE\n")
	dstDir := t.TempDir()

	// Override short-circuits os-release generation (empty os-release is fine),
	// matching WriteReleaseFile semantics: bytes land verbatim at the release path.
	if err := PackageDirectory(srcDir, dstDir, "foo", strings.NewReader(""), TypeSysext, override); err != nil {
		t.Fatalf("PackageDirectory with override: %v", err)
	}

	want := map[string]string{
		"usr/bin/tool": "x\n",
		filepath.FromSlash(releasePath(TypeSysext, "foo")): string(override),
	}
	assertFileSet(t, dstDir, want)
}
