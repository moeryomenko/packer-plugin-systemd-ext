// Contract tests for version.PluginVersion.
//
// Expected red state against the empty scaffold: this file does NOT compile
// because version/version.go does not exist yet (PluginVersion is undefined).
// That compile failure is the intended red phase — it is resolved only when
// version/version.go and version/VERSION are created.
//
// The tests must run from the version/ package directory (the working
// directory of `go test ./version/`), which is why the VERSION file is read
// by its bare name.

package version

import (
	"os"
	"strings"
	"testing"
)

// pluginVersionString returns the source version string that the SDK object
// was built from. The SDK's PluginVersion.String() canonicalizes the version
// and strips a leading "v" (v0.1.0-dev => 0.1.0-dev), so the v-prefixed
// contract is asserted against SemVer().Original(), which preserves the
// original input exactly.
func pluginVersionString(t *testing.T) string {
	t.Helper()
	if PluginVersion == nil {
		t.Fatal("version.PluginVersion is nil; it must be defined via version.NewPluginVersion")
	}
	return PluginVersion.SemVer().Original()
}

// readVERSION returns the trimmed content of version/VERSION.
func readVERSION(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("VERSION")
	if err != nil {
		t.Fatalf("cannot read version/VERSION: %v (scaffold incomplete)", err)
	}
	return strings.TrimSpace(string(raw))
}

func TestPluginVersionIsSet(t *testing.T) {
	if v := strings.TrimSpace(pluginVersionString(t)); v == "" {
		t.Fatal("version.PluginVersion has an empty version string")
	}
}

func TestPluginVersionVPrefix(t *testing.T) {
	v := pluginVersionString(t)
	if !strings.HasPrefix(v, "v") {
		t.Fatalf("version.PluginVersion = %q, want a version starting with \"v\" (e.g. v0.1.0-dev)", v)
	}
}

func TestPluginVersionMatchesVERSION(t *testing.T) {
	got := pluginVersionString(t)
	want := readVERSION(t)
	if got != want {
		t.Fatalf("version.PluginVersion = %q does not match version/VERSION content %q", got, want)
	}
}
