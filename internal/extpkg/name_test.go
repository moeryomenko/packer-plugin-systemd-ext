// Contract tests for extpkg extension-name validation.
//
// This file defines the public API contract for the shared name rules used by
// both provisioners' Prepare-time validation: an empty name, a name longer
// than 255 bytes, or a name not matching ^[A-Za-z0-9._-]+$ is invalid). The
// exact signature is authored here so the engineer implements to match:
//
//	func ValidateName(name string) error
//
// Expected red state against the current scaffold: internal/extpkg contains
// only doc.go, so this file does NOT compile (undefined: extpkg.ValidateName).
// That compile failure is the intended red phase; it is resolved when
// internal/extpkg/name.go is implemented.
//
// Contract pinned by these tests:
//   - ValidateName returns nil iff name is non-empty, at most 255 bytes long,
//     every byte is in [A-Za-z0-9._-], and name is not exactly "..".
//   - Otherwise it returns a non-nil error with a non-empty message; the
//     message mentions "name" for emptiness/character problems, "255" for the
//     length limit, and echoes the offending input (TestValidateNameErrorNamesInput).
//   - The length limit is measured in bytes (len(name)).

package extpkg

import (
	"strings"
	"testing"
)

func TestValidateNameValid(t *testing.T) {
	valid := []string{
		"foo",
		"a", // minimum length
		"A",
		"0",
		".hidden",   // leading dot is allowed by the character class
		"trailing.", // trailing dot is allowed by the character class
		"-dash",
		"_under",
		"dot.dot",
		"mix-ed.v1_2",
		".",                      // single dot is not "..", so it is valid
		strings.Repeat("a", 255), // upper length bound
	}
	for _, name := range valid {
		name := name
		t.Run(name, func(t *testing.T) {
			if err := ValidateName(name); err != nil {
				t.Fatalf("ValidateName(%q) = %v, want nil", name, err)
			}
		})
	}
}

func TestValidateNameInvalid(t *testing.T) {
	invalid := []struct {
		name string
		want string // substring the error message must contain
	}{
		{name: "", want: "name"},                      // empty
		{name: "..", want: "name"},                    // parent dir
		{name: "/", want: "name"},                     // path separator
		{name: "a/b", want: "name"},                   // path separator
		{name: "a b", want: "name"},                   // space
		{name: "a\tb", want: "name"},                  // tab
		{name: "a\nb", want: "name"},                  // newline
		{name: "a:b", want: "name"},                   // outside character class
		{name: "a\\b", want: "name"},                  // outside character class
		{name: "a#b", want: "name"},                   // outside character class
		{name: "aé", want: "name"},                    // non-ASCII, outside character class
		{name: strings.Repeat("a", 256), want: "255"}, // one byte over the limit
	}
	for _, tc := range invalid {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateName(tc.name)
			if err == nil {
				t.Fatalf("ValidateName(%q) = nil, want error", tc.name)
			}
			msg := err.Error()
			if msg == "" {
				t.Fatalf("ValidateName(%q) returned an empty error message", tc.name)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("ValidateName(%q) error %q does not mention %q", tc.name, msg, tc.want)
			}
		})
	}
}

func TestValidateNameErrorNamesInput(t *testing.T) {
	// A "clear error" (task brief) echoes the offending name so the caller can
	// aggregate it into the field+extension error message.
	err := ValidateName("a b")
	if err == nil {
		t.Fatal(`ValidateName("a b") = nil, want error`)
	}
	if msg := err.Error(); !strings.Contains(msg, "a b") {
		t.Fatalf("ValidateName error %q does not echo the offending name %q", msg, "a b")
	}
}
