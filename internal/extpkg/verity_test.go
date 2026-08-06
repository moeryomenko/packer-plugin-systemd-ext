// Contract tests for extpkg verity companion-set detection for prebuilt .raw
// extension images.
//
// This file defines the public API contract for companion discovery; the
// engineer implements to match:
//
//	func DetectVeritySet(basePath string) ([]string, error)
//
// basePath is the path of the .raw source (e.g. /tmp/x/foo.raw). The function
// scans the same directory for the companions of the base name
// (basename(basePath) minus ".raw"): <base>.verity, <base>.roothash and
// <base>.roothash.p7s, and returns the list of companion PATHS to upload.
//
// Expected red state against the current repo: internal/extpkg has name.go +
// release.go but no verity detection, so this file does NOT compile
// (undefined: extpkg.DetectVeritySet). That compile failure is the intended
// red phase; it is resolved when internal/extpkg/verity.go is implemented.
//
// Contract pinned by these tests:
//   - No companions next to the .raw is valid: nil error, zero companions.
//   - foo.raw + foo.verity + foo.roothash is a complete set: returns both
//     companion paths, nil error. foo.roothash.p7s is optional: adding it is
//     still complete and all three paths are returned.
//   - If ANY expected companion exists but the set is incomplete, the error
//     contains the code "INCOMPLETE_VERITY_SET" and names the missing file
//     (the basename of the absent companion, e.g. "foo.roothash").
//   - Completeness rules: .verity and .roothash are a pair (one without the
//     other is incomplete); .roothash.p7s signs the root hash and therefore
//     requires .roothash (a signature without its roothash is incomplete).
//   - Companions with a different base name are ignored: foo.verity next to a
//     bar.raw source is not part of bar's set and never triggers an error.
//   - The returned paths are absolute paths in the .raw's directory (the
//     companion files the caller must upload); the order is unspecified.

package extpkg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// mustTouch creates an empty regular file at path for fixture purposes.
func mustTouch(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), 0o644); err != nil {
		t.Fatalf("create fixture %s: %v", path, err)
	}
}

// sameStringSet reports whether got and want contain the same strings
// (order-insensitive).
func sameStringSet(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	ws := make(map[string]struct{}, len(want))
	for _, w := range want {
		ws[w] = struct{}{}
	}
	for _, g := range got {
		if _, ok := ws[g]; !ok {
			return false
		}
	}
	return true
}

func TestDetectVeritySet(t *testing.T) {
	tests := []struct {
		name           string
		raw            string   // basename of the .raw source to create
		companions     []string // basenames of companion files to create next to it
		wantCompanions []string // expected returned basenames (success cases)
		wantErr        bool     // expect INCOMPLETE_VERITY_SET naming wantErrName
		wantErrName    string
	}{
		{
			name: "bare raw is valid",
			raw:  "foo.raw",
		},
		{
			name:           "complete pair",
			raw:            "foo.raw",
			companions:     []string{"foo.verity", "foo.roothash"},
			wantCompanions: []string{"foo.verity", "foo.roothash"},
		},
		{
			name:           "complete signed set",
			raw:            "foo.raw",
			companions:     []string{"foo.verity", "foo.roothash", "foo.roothash.p7s"},
			wantCompanions: []string{"foo.verity", "foo.roothash", "foo.roothash.p7s"},
		},
		{
			name:        "missing roothash",
			raw:         "foo.raw",
			companions:  []string{"foo.verity"},
			wantErr:     true,
			wantErrName: "foo.roothash",
		},
		{
			name:        "missing verity",
			raw:         "foo.raw",
			companions:  []string{"foo.roothash"},
			wantErr:     true,
			wantErrName: "foo.verity",
		},
		{
			name:        "signature without roothash",
			raw:         "foo.raw",
			companions:  []string{"foo.roothash.p7s"},
			wantErr:     true,
			wantErrName: "foo.roothash",
		},
		{
			name:       "wrong base companions ignored",
			raw:        "bar.raw",
			companions: []string{"foo.verity", "foo.roothash"},
		},
		{
			name:           "wrong base ignored alongside complete set",
			raw:            "bar.raw",
			companions:     []string{"bar.verity", "bar.roothash", "foo.verity"},
			wantCompanions: []string{"bar.verity", "bar.roothash"},
		},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			base := filepath.Join(dir, tt.raw)
			mustTouch(t, base)
			for _, c := range tt.companions {
				mustTouch(t, filepath.Join(dir, c))
			}

			got, err := DetectVeritySet(base)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("DetectVeritySet(%s) = %v, nil; want INCOMPLETE_VERITY_SET error", base, got)
				}
				if !strings.Contains(err.Error(), "INCOMPLETE_VERITY_SET") {
					t.Fatalf("error %q does not contain code %q", err.Error(), "INCOMPLETE_VERITY_SET")
				}
				if !strings.Contains(err.Error(), tt.wantErrName) {
					t.Fatalf("error %q does not name missing file %q", err.Error(), tt.wantErrName)
				}
				return
			}
			if err != nil {
				t.Fatalf("DetectVeritySet(%s): unexpected error %v", base, err)
			}
			var want []string
			for _, c := range tt.wantCompanions {
				want = append(want, filepath.Join(dir, c))
			}
			if !sameStringSet(got, want) {
				t.Fatalf("DetectVeritySet(%s) = %v, want set %v", base, got, want)
			}
		})
	}
}
