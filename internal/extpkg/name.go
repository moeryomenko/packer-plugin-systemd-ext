package extpkg

import (
	"fmt"
	"regexp"
)

// nameRe matches the character class allowed in extension names: ASCII
// letters, digits, dot, underscore, and dash.
var nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)

// ValidateName reports whether name is a valid extension name: non-empty, at
// most 255 bytes, matching ^[A-Za-z0-9._-]+$, and not exactly ".." (a ".."
// name would let the release file escape its directory). The returned error
// names the failing rule and echoes the offending input.
func ValidateName(name string) error {
	if name == "" {
		return fmt.Errorf("name: empty extension name")
	}
	if len(name) > 255 {
		return fmt.Errorf("name %q: exceeds 255 bytes", name)
	}
	if name == ".." {
		return fmt.Errorf("name %q: must not be %q", name, "..")
	}
	if !nameRe.MatchString(name) {
		return fmt.Errorf("name %q: contains invalid characters", name)
	}
	return nil
}
