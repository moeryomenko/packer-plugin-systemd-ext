// Layout contract tests for the repository layout.
//
// Expected red state against the empty scaffold: the repository has no go.mod
// yet, so `go test .` fails before any test runs with
// "go: cannot find main module" — that is the intended red phase. Once
// go.mod exists, this file compiles as a test-only package and fails on
// whichever directory or module-path assertion is still unmet.
//
// The tests run from the repository root (the working directory of
// `go test .`), so paths are relative to the root.

package main

import (
	"os"
	"strings"
	"testing"
)

// repoLayoutDirs are the required package directories.
var repoLayoutDirs = []string{
	"provisioner/sysext",
	"provisioner/confext",
	"internal/extpkg",
	"internal/guestops",
	"version",
	"docs",
}

func TestScaffoldLayoutDirectoriesExist(t *testing.T) {
	for _, dir := range repoLayoutDirs {
		info, err := os.Stat(dir)
		if err != nil {
			t.Errorf("required package directory %q missing: %v (scaffold incomplete)", dir, err)
			continue
		}
		if !info.IsDir() {
			t.Errorf("required package directory %q exists but is not a directory", dir)
		}
	}
}

func TestScaffoldGoModModulePath(t *testing.T) {
	raw, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatalf("cannot read go.mod: %v (scaffold incomplete)", err)
	}

	modulePath := ""
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "module ") {
			modulePath = strings.TrimPrefix(line, "module ")
			break
		}
	}
	if modulePath == "" {
		t.Fatalf("go.mod does not contain a module directive; got:\n%s", string(raw))
	}
	if !strings.HasSuffix(modulePath, "packer-plugin-systemd-ext") {
		t.Fatalf("go.mod module path %q does not end in %q", modulePath, "packer-plugin-systemd-ext")
	}
}
