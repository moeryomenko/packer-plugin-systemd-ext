// Contract tests for extpkg release-file generation for systemd-sysext and
// systemd-confext extension images.
//
// This file defines the public API contract for release generation; the
// engineer implements to match:
//
//	type ExtensionType int
//	const (
//	    TypeSysext ExtensionType = iota
//	    TypeConfext
//	)
//	func WriteReleaseFile(dstDir, name string, osRelease io.Reader, typ ExtensionType, override []byte) error
//
// Expected red state against the current scaffold: internal/extpkg contains
// only doc.go, so this file does NOT compile (undefined: extpkg.WriteReleaseFile,
// extpkg.ExtensionType, extpkg.TypeSysext, extpkg.TypeConfext). That compile
// failure is the intended red phase; it is resolved when
// internal/extpkg/release.go is implemented.
//
// Contract pinned by these tests:
//   - dstDir is the root of the packaged extension tree. The release file is
//     created at dstDir/usr/lib/extension-release.d/extension-release.<name>
//     for TypeSysext and dstDir/etc/extension-release.d/extension-release.<name>
//     for TypeConfext (intermediate directories are created as needed).
//   - Generated content is exact and deterministic: "ID=<value>\n" then
//     "VERSION_ID=<value>\n", then "SYSEXT_LEVEL=<value>\n" (sysext) or
//     "CONFEXT_LEVEL=<value>\n" (confext) when the corresponding field is
//     present in the input. No other os-release fields are copied. The output
//     always ends with a trailing newline.
//   - os-release parsing follows os-release(5): "KEY=value" lines, optional
//     double or single quotes around the value (stripped in the output),
//     "#" comment lines and blank lines ignored, CRLF line endings tolerated,
//     lines without "=" ignored, trailing newline not required in the input.
//   - ID= and VERSION_ID= are required: if either is absent or empty, an error
//     naming the missing field is returned and no file is written.
//   - override: nil means "generate from os-release"; non-nil with len 0 is an
//     error (message mentions "override"); non-empty override is written
//     byte-for-byte at the same path with no normalization (no trailing
//     newline is appended, os-release is not parsed).
//   - name must satisfy ValidateName, otherwise an error mentioning "name" is
//     returned (a "/" or ".." name would escape the release directory).
//   - typ must be TypeSysext or TypeConfext, otherwise an error mentioning
//     "type" is returned.
//   - Any error reading osRelease is propagated unchanged.

package extpkg

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// releasePath returns the relative release-file path the contract requires
// for typ/name. It mirrors the documented layout so tests can assert placement.
func releasePath(typ ExtensionType, name string) string {
	switch typ {
	case TypeSysext:
		return "usr/lib/extension-release.d/extension-release." + name
	case TypeConfext:
		return "etc/extension-release.d/extension-release." + name
	}
	return ""
}

func releaseTypeName(typ ExtensionType) string {
	switch typ {
	case TypeSysext:
		return "sysext"
	case TypeConfext:
		return "confext"
	}
	return "unknown"
}

// writeRelease calls WriteReleaseFile with osRelease as a string reader.
func writeRelease(t *testing.T, dstDir, name, osRelease string, typ ExtensionType, override []byte) error {
	t.Helper()
	return WriteReleaseFile(dstDir, name, strings.NewReader(osRelease), typ, override)
}

// readReleaseFile returns the bytes at the contract path for typ/name under
// dstDir, failing the test if the file is missing.
func readReleaseFile(t *testing.T, dstDir, name string, typ ExtensionType) []byte {
	t.Helper()
	p := filepath.Join(dstDir, filepath.FromSlash(releasePath(typ, name)))
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read release file %s: %v", p, err)
	}
	return b
}

// assertReleaseFileAbsent fails the test if a file exists at the contract path.
func assertReleaseFileAbsent(t *testing.T, dstDir, name string, typ ExtensionType) {
	t.Helper()
	p := filepath.Join(dstDir, filepath.FromSlash(releasePath(typ, name)))
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Fatalf("expected no release file at %s, stat err = %v", p, err)
	}
}

const fixtureBasic = `NAME="Ubuntu"
ID=ubuntu
VERSION_ID=24.04
VERSION_CODENAME=noble
PRETTY_NAME="Ubuntu 24.04.1 LTS"
`

func TestWriteReleaseFileSysextBasic(t *testing.T) {
	dstDir := t.TempDir()
	if err := writeRelease(t, dstDir, "foo", fixtureBasic, TypeSysext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
		t.Fatalf("sysext release file = %q, want %q", got, want)
	}
}

func TestWriteReleaseFileSysextWithLevel(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "NAME=\"Ubuntu\"\nID=ubuntu\nVERSION_ID=24.04\nSYSEXT_LEVEL=x\n"
	if err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\nSYSEXT_LEVEL=x\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
		t.Fatalf("sysext release file = %q, want %q", got, want)
	}
}

func TestWriteReleaseFileConfextBasic(t *testing.T) {
	dstDir := t.TempDir()
	if err := writeRelease(t, dstDir, "foo", fixtureBasic, TypeConfext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeConfext)); got != want {
		t.Fatalf("confext release file = %q, want %q", got, want)
	}
}

func TestWriteReleaseFileConfextWithLevel(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "ID=ubuntu\nVERSION_ID=24.04\nCONFEXT_LEVEL=y\n"
	if err := writeRelease(t, dstDir, "foo", fixture, TypeConfext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\nCONFEXT_LEVEL=y\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeConfext)); got != want {
		t.Fatalf("confext release file = %q, want %q", got, want)
	}
}

func TestWriteReleaseFileOverrideVerbatim(t *testing.T) {
	override := []byte("MY-CUSTOM-RELEASE\nSECOND-LINE\n")
	for _, typ := range []ExtensionType{TypeSysext, TypeConfext} {
		typ := typ
		t.Run(releaseTypeName(typ), func(t *testing.T) {
			dstDir := t.TempDir()
			// Override short-circuits generation: empty os-release is fine.
			if err := writeRelease(t, dstDir, "foo", "", typ, override); err != nil {
				t.Fatalf("WriteReleaseFile: %v", err)
			}
			if got := readReleaseFile(t, dstDir, "foo", typ); !bytes.Equal(got, override) {
				t.Fatalf("%s override file = %q, want verbatim %q", releaseTypeName(typ), got, override)
			}
		})
	}
}

func TestWriteReleaseFileOverrideWithoutTrailingNewline(t *testing.T) {
	// Override wins over the trailing-newline rule: bytes are preserved exactly.
	override := []byte("ID=x\nVERSION_ID=y")
	dstDir := t.TempDir()
	if err := writeRelease(t, dstDir, "foo", fixtureBasic, TypeSysext, override); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	if got := readReleaseFile(t, dstDir, "foo", TypeSysext); !bytes.Equal(got, override) {
		t.Fatalf("override file = %q, want verbatim %q (no trailing newline added)", got, override)
	}
}

func TestWriteReleaseFileEmptyOverride(t *testing.T) {
	dstDir := t.TempDir()
	err := writeRelease(t, dstDir, "foo", fixtureBasic, TypeSysext, []byte{})
	if err == nil {
		t.Fatal("WriteReleaseFile with empty non-nil override = nil, want error (no empty file)")
	}
	if msg := err.Error(); !strings.Contains(msg, "override") {
		t.Fatalf("empty-override error %q does not mention %q", msg, "override")
	}
	assertReleaseFileAbsent(t, dstDir, "foo", TypeSysext)
}

func TestWriteReleaseFileMissingID(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "NAME=\"Ubuntu\"\nVERSION_ID=24.04\n"
	err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil)
	if err == nil {
		t.Fatal("WriteReleaseFile with missing ID = nil, want error")
	}
	if msg := err.Error(); !strings.Contains(msg, "ID") {
		t.Fatalf("missing-ID error %q does not mention %q", msg, "ID")
	}
	assertReleaseFileAbsent(t, dstDir, "foo", TypeSysext)
}

func TestWriteReleaseFileMissingVersionID(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "NAME=\"Ubuntu\"\nID=ubuntu\n"
	err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil)
	if err == nil {
		t.Fatal("WriteReleaseFile with missing VERSION_ID = nil, want error")
	}
	if msg := err.Error(); !strings.Contains(msg, "VERSION_ID") {
		t.Fatalf("missing-VERSION_ID error %q does not mention %q", msg, "VERSION_ID")
	}
	assertReleaseFileAbsent(t, dstDir, "foo", TypeSysext)
}

func TestWriteReleaseFileEmptyOSRelease(t *testing.T) {
	dstDir := t.TempDir()
	err := writeRelease(t, dstDir, "foo", "", TypeSysext, nil)
	if err == nil {
		t.Fatal("WriteReleaseFile with empty os-release = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "ID") && !strings.Contains(msg, "VERSION_ID") {
		t.Fatalf("empty os-release error %q does not name a required field", msg)
	}
	assertReleaseFileAbsent(t, dstDir, "foo", TypeSysext)
}

func TestWriteReleaseFileQuotedValues(t *testing.T) {
	cases := []struct {
		name    string
		fixture string
	}{
		{name: "double-quoted", fixture: "ID=\"ubuntu\"\nVERSION_ID=\"24.04\"\n"},
		{name: "single-quoted", fixture: "ID='ubuntu'\nVERSION_ID='24.04'\n"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			dstDir := t.TempDir()
			if err := writeRelease(t, dstDir, "foo", tc.fixture, TypeSysext, nil); err != nil {
				t.Fatalf("WriteReleaseFile: %v", err)
			}
			want := "ID=ubuntu\nVERSION_ID=24.04\n"
			if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
				t.Fatalf("release file = %q, want %q (quotes stripped)", got, want)
			}
		})
	}
}

func TestWriteReleaseFileTrailingNewlineAdded(t *testing.T) {
	// Input without a trailing newline still produces output with one.
	dstDir := t.TempDir()
	fixture := "ID=ubuntu\nVERSION_ID=24.04"
	if err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
		t.Fatalf("release file = %q, want %q (trailing newline required)", got, want)
	}
}

func TestWriteReleaseFileCRLFNormalized(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "ID=ubuntu\r\nVERSION_ID=24.04\r\n"
	if err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
		t.Fatalf("release file = %q, want %q (CRLF normalized)", got, want)
	}
}

func TestWriteReleaseFileCommentsAndBlankLines(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "# generated for the golden image\n\nID=ubuntu\n\nVERSION_ID=24.04\n"
	if err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
		t.Fatalf("release file = %q, want %q (comments and blank lines dropped)", got, want)
	}
}

func TestWriteReleaseFileLevelIsolation(t *testing.T) {
	fixture := "ID=ubuntu\nVERSION_ID=24.04\nSYSEXT_LEVEL=x\nCONFEXT_LEVEL=y\n"
	cases := []struct {
		typ     ExtensionType
		wantIn  string
		wantOut string
	}{
		{typ: TypeSysext, wantIn: "SYSEXT_LEVEL=x", wantOut: "CONFEXT_LEVEL"},
		{typ: TypeConfext, wantIn: "CONFEXT_LEVEL=y", wantOut: "SYSEXT_LEVEL"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(releaseTypeName(tc.typ), func(t *testing.T) {
			dstDir := t.TempDir()
			if err := writeRelease(t, dstDir, "foo", fixture, tc.typ, nil); err != nil {
				t.Fatalf("WriteReleaseFile: %v", err)
			}
			got := readReleaseFile(t, dstDir, "foo", tc.typ)
			if !bytes.Contains(got, []byte(tc.wantIn)) {
				t.Fatalf("%s release file %q does not contain %q", releaseTypeName(tc.typ), got, tc.wantIn)
			}
			if bytes.Contains(got, []byte(tc.wantOut)) {
				t.Fatalf("%s release file %q must not contain the other type's level field %q", releaseTypeName(tc.typ), got, tc.wantOut)
			}
		})
	}
}

func TestWriteReleaseFileMalformedLineSkipped(t *testing.T) {
	dstDir := t.TempDir()
	fixture := "HELLO WORLD\nID=ubuntu\nVERSION_ID=24.04\n"
	if err := writeRelease(t, dstDir, "foo", fixture, TypeSysext, nil); err != nil {
		t.Fatalf("WriteReleaseFile: %v", err)
	}
	want := "ID=ubuntu\nVERSION_ID=24.04\n"
	if got := string(readReleaseFile(t, dstDir, "foo", TypeSysext)); got != want {
		t.Fatalf("release file = %q, want %q (line without '=' ignored)", got, want)
	}
}

func TestWriteReleaseFileEmptyValueRequiredField(t *testing.T) {
	cases := []struct {
		fixture string
		want    string // substring the error message must contain
	}{
		{fixture: "ID=ubuntu\nVERSION_ID=\n", want: "VERSION_ID"}, // field present but empty
		{fixture: "ID=\nVERSION_ID=24.04\n", want: "ID"},          // field present but empty
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.want, func(t *testing.T) {
			dstDir := t.TempDir()
			err := writeRelease(t, dstDir, "foo", tc.fixture, TypeSysext, nil)
			if err == nil {
				t.Fatalf("WriteReleaseFile with empty %s = nil, want error", tc.want)
			}
			if msg := err.Error(); !strings.Contains(msg, tc.want) {
				t.Fatalf("error %q does not mention %q", msg, tc.want)
			}
			assertReleaseFileAbsent(t, dstDir, "foo", TypeSysext)
		})
	}
}

func TestWriteReleaseFileInvalidName(t *testing.T) {
	names := []string{"", "a/b", "..", strings.Repeat("a", 256)}
	for _, name := range names {
		name := name
		t.Run(name, func(t *testing.T) {
			dstDir := t.TempDir()
			err := writeRelease(t, dstDir, name, fixtureBasic, TypeSysext, nil)
			if err == nil {
				t.Fatalf("WriteReleaseFile with name %q = nil, want error", name)
			}
			if msg := err.Error(); !strings.Contains(msg, "name") {
				t.Fatalf("invalid-name error %q does not mention %q", msg, "name")
			}
			assertReleaseFileAbsent(t, dstDir, name, TypeSysext)
		})
	}
}

func TestWriteReleaseFileInvalidType(t *testing.T) {
	dstDir := t.TempDir()
	err := WriteReleaseFile(dstDir, "foo", strings.NewReader(fixtureBasic), ExtensionType(99), nil)
	if err == nil {
		t.Fatal("WriteReleaseFile with ExtensionType(99) = nil, want error")
	}
	if msg := err.Error(); !strings.Contains(msg, "type") {
		t.Fatalf("invalid-type error %q does not mention %q", msg, "type")
	}
}

// failingReader simulates an os-release source that fails mid-read.
type failingReader struct{ err error }

func (r failingReader) Read([]byte) (int, error) { return 0, r.err }

func TestWriteReleaseFileReaderErrorPropagated(t *testing.T) {
	dstDir := t.TempDir()
	boom := errors.New("boom")
	err := WriteReleaseFile(dstDir, "foo", failingReader{err: boom}, TypeSysext, nil)
	if err == nil {
		t.Fatal("WriteReleaseFile with failing reader = nil, want error")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Fatalf("reader error was not propagated: %v", err)
	}
	assertReleaseFileAbsent(t, dstDir, "foo", TypeSysext)
}
