// Contract tests for the confext provisioner's Config decoding and
// Prepare-time validation.
//
// These tests pin the provisioner contract. They are same-package tests so
// they can observe the decoded config (defaults) exactly as the reference
// packer-plugin-ansible tests do. The required API surface is pinned here;
// the engineer implements to match:
//
//	type Extension struct {
//		Name        string `mapstructure:"name"`
//		Source      string `mapstructure:"source"`
//		Format      string `mapstructure:"format"`
//		ReleaseFile string `mapstructure:"release_file"`
//	}
//
//	type Config struct {
//		common.PackerConfig `mapstructure:",squash"`
//		Extensions       []Extension `mapstructure:"extensions"`
//		Mode             string      `mapstructure:"mode"`
//		MergeDuringBuild bool        `mapstructure:"merge_during_build"`
//		Command          string      `mapstructure:"command"`
//		GuestInstallDir  string      `mapstructure:"guest_install_dir"`
//	}
//
//	type Provisioner struct {
//		config Config
//	}
//
//	func (p *Provisioner) Prepare(raws ...interface{}) error
//	func (p *Provisioner) ConfigSpec() hcldec.ObjectSpec
//
// Defaults: mode=bake, merge_during_build=true, command and guest_install_dir
// below. Prepare applies the defaults when the fields are
// absent OR explicitly empty; merge_during_build defaults to true only when
// the key is absent (false is a meaningful explicit value).
//
// Expected red state against the current stub: provisioner.go has an empty
// Config (only common.PackerConfig), an empty Provisioner, ConfigSpec returns
// an empty spec, and Prepare returns nil. This file does NOT compile against
// the stub (p.config and the Config fields do not exist yet). That compile
// failure is the intended red phase; it resolves when the stub is filled in.
//
// Note on the name "..": the regex ^[A-Za-z0-9._-]+$ would admit "..", but
// the plan and the already-delivered internal/extpkg name contract reject it.
// These tests require Prepare to
// reject ".." too, i.e. the implementer must add a ".." exclusion on top of
// the character-class check. Flagged to @build as a spec ambiguity.
package confext

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// wantDefaultCommand and wantDefaultInstallDir are the defaults for
// the confext provisioner.
const (
	wantDefaultCommand    = "systemd-confext"
	wantDefaultInstallDir = "/var/lib/confexts"
)

// extension returns one extension-entry map. name/source must be supplied;
// format and release_file default to their zero values.
func extension(name, source string) map[string]interface{} {
	return map[string]interface{}{
		"name":   name,
		"source": source,
	}
}

// minimalConfig returns a config map with exactly one valid extension whose
// source is a real temp directory and with all defaulted fields omitted.
// Callers override keys to inject a single violation.
func minimalConfig(t *testing.T) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"extensions": []map[string]interface{}{
			extension("extone", t.TempDir()),
		},
	}
}

// writeRawSource creates an (empty) file named <name>.raw under a temp dir
// and returns its path. An existing file with the .raw suffix is a valid
// source regardless of size.
func writeRawSource(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name+".raw")
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatalf("write raw source %s: %v", p, err)
	}
	return p
}

func TestProvisioner_Impl(t *testing.T) {
	var raw interface{} = &Provisioner{}
	if _, ok := raw.(packersdk.Provisioner); !ok {
		t.Fatalf("confext Provisioner must be a packersdk.Provisioner")
	}
}

func TestPrepareMinimalValidConfigAppliesDefaults(t *testing.T) {
	var p Provisioner
	if err := p.Prepare(minimalConfig(t)); err != nil {
		t.Fatalf("Prepare(minimal valid config) = %v, want nil", err)
	}
	if p.config.Mode != "bake" {
		t.Errorf("default mode = %q, want %q", p.config.Mode, "bake")
	}
	if !p.config.MergeDuringBuild {
		t.Errorf("default merge_during_build = false, want true")
	}
	if p.config.Command != wantDefaultCommand {
		t.Errorf("default command = %q, want %q", p.config.Command, wantDefaultCommand)
	}
	if p.config.GuestInstallDir != wantDefaultInstallDir {
		t.Errorf("default guest_install_dir = %q, want %q", p.config.GuestInstallDir, wantDefaultInstallDir)
	}
}

func TestPrepareExplicitEmptyStringsStillGetDefaults(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["mode"] = ""
	cfg["command"] = ""
	cfg["guest_install_dir"] = ""
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare with empty-string fields = %v, want nil", err)
	}
	if p.config.Mode != "bake" {
		t.Errorf("mode after empty string = %q, want %q", p.config.Mode, "bake")
	}
	if p.config.Command != wantDefaultCommand {
		t.Errorf("command after empty string = %q, want %q", p.config.Command, wantDefaultCommand)
	}
	if p.config.GuestInstallDir != wantDefaultInstallDir {
		t.Errorf("guest_install_dir after empty string = %q, want %q", p.config.GuestInstallDir, wantDefaultInstallDir)
	}
}

func TestPrepareDecodesExplicitFields(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["mode"] = "persist"
	cfg["merge_during_build"] = false
	cfg["command"] = "/usr/bin/" + wantDefaultCommand
	cfg["guest_install_dir"] = "/custom/dir"
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(explicit fields) = %v, want nil", err)
	}
	if p.config.Mode != "persist" {
		t.Errorf("mode = %q, want %q", p.config.Mode, "persist")
	}
	if p.config.MergeDuringBuild {
		t.Errorf("merge_during_build = true, want false")
	}
	if p.config.Command != "/usr/bin/"+wantDefaultCommand {
		t.Errorf("command = %q, want %q", p.config.Command, "/usr/bin/"+wantDefaultCommand)
	}
	if p.config.GuestInstallDir != "/custom/dir" {
		t.Errorf("guest_install_dir = %q, want %q", p.config.GuestInstallDir, "/custom/dir")
	}
	if len(p.config.Extensions) != 1 {
		t.Errorf("len(extensions) = %d, want 1", len(p.config.Extensions))
	}
	if p.config.Extensions[0].Name != "extone" {
		t.Errorf("extensions[0].name = %q, want %q", p.config.Extensions[0].Name, "extone")
	}
}

func TestPrepareEmptyExtensions(t *testing.T) {
	tests := []struct {
		name string
		cfg  map[string]interface{}
	}{
		{name: "missing key", cfg: map[string]interface{}{}},
		{name: "empty list", cfg: map[string]interface{}{"extensions": []map[string]interface{}{}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Provisioner
			err := p.Prepare(tt.cfg)
			if err == nil {
				t.Fatalf("Prepare with %s = nil, want error", tt.name)
			}
			if !strings.Contains(err.Error(), "extensions") {
				t.Fatalf("error %q does not name field %q", err.Error(), "extensions")
			}
		})
	}
}

func TestPrepareInvalidMode(t *testing.T) {
	// Values are case-sensitive; only exactly "bake" or "persist".
	for _, mode := range []string{"BAKE", "Persist", "baked", "bake "} {
		t.Run(mode, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["mode"] = mode
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare with mode %q = nil, want error", mode)
			}
			if !strings.Contains(err.Error(), "mode") {
				t.Fatalf("error %q does not name field %q", err.Error(), "mode")
			}
		})
	}
}

func TestPrepareValidModePersist(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["mode"] = "persist"
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(mode=persist) = %v, want nil", err)
	}
	if p.config.Mode != "persist" {
		t.Errorf("mode = %q, want %q", p.config.Mode, "persist")
	}
}

func TestPrepareInvalidName(t *testing.T) {
	longName := strings.Repeat("a", 256)
	tests := []struct {
		name    string
		extName string
		want    string // substring the error message must contain
	}{
		{name: "empty", extName: "", want: "name"},
		{name: "slash", extName: "bad/name", want: "bad/name"},
		{name: "dotdot", extName: "..", want: ".."},
		{name: "space", extName: "bad name", want: "bad name"},
		// A tab is escaped by %q-style error formatting, so only the field
		// name is guaranteed to appear; the value may be echoed escaped.
		{name: "tab", extName: "bad\tname", want: "name"},
		{name: "too long", extName: longName, want: "255"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["extensions"] = []map[string]interface{}{
				{"name": tt.extName, "source": t.TempDir()},
			}
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare with name %q = nil, want error", tt.extName)
			}
			msg := err.Error()
			if !strings.Contains(msg, "name") {
				t.Fatalf("error %q does not name field %q", msg, "name")
			}
			if !strings.Contains(msg, tt.want) {
				t.Fatalf("error %q does not mention %q", msg, tt.want)
			}
		})
	}
}

func TestPrepareValidNames(t *testing.T) {
	names := []string{
		"a",
		"A",
		"0",
		"foo",
		"good.name-v1_2",
		strings.Repeat("a", 255), // upper byte bound
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["extensions"] = []map[string]interface{}{
				{"name": name, "source": t.TempDir()},
			}
			if err := p.Prepare(cfg); err != nil {
				t.Fatalf("Prepare with name %q = %v, want nil", name, err)
			}
		})
	}
}

func TestPrepareInvalidSource(t *testing.T) {
	plainFile := filepath.Join(t.TempDir(), "payload.txt")
	if err := os.WriteFile(plainFile, []byte("x"), 0o644); err != nil {
		t.Fatalf("write plain file: %v", err)
	}
	tests := []struct {
		name   string
		source string
	}{
		{name: "empty source", source: ""},
		{name: "nonexistent path", source: filepath.Join(t.TempDir(), "missing")},
		{name: "file without raw suffix", source: plainFile},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["extensions"] = []map[string]interface{}{
				{"name": "extone", "source": tt.source},
			}
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare with source %q = nil, want error", tt.source)
			}
			msg := err.Error()
			if !strings.Contains(msg, "source") {
				t.Fatalf("error %q does not name field %q", msg, "source")
			}
			if !strings.Contains(msg, "extone") {
				t.Fatalf("error %q does not name the extension %q", msg, "extone")
			}
		})
	}
}

func TestPrepareValidRawSource(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["extensions"] = []map[string]interface{}{
		{"name": "rawimg", "source": writeRawSource(t, "rawimg")},
	}
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(raw source) = %v, want nil", err)
	}
}

func TestPrepareRawSourceIgnoresFormat(t *testing.T) {
	// Format applies only to directory sources; a bogus format on a .raw
	// source must not fail validation.
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["extensions"] = []map[string]interface{}{
		{"name": "rawimg", "source": writeRawSource(t, "rawimg"), "format": "definitely-not-a-format"},
	}
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(raw source with bogus format) = %v, want nil", err)
	}
}

func TestPrepareInvalidFormat(t *testing.T) {
	for _, format := range []string{"gzip", "tar", "squash"} {
		t.Run(format, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["extensions"] = []map[string]interface{}{
				{"name": "extone", "source": t.TempDir(), "format": format},
			}
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare with format %q = nil, want error", format)
			}
			msg := err.Error()
			if !strings.Contains(msg, "format") {
				t.Fatalf("error %q does not name field %q", msg, "format")
			}
			if !strings.Contains(msg, "extone") {
				t.Fatalf("error %q does not name the extension %q", msg, "extone")
			}
		})
	}
}

func TestPrepareValidFormatDirectory(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["extensions"] = []map[string]interface{}{
		{"name": "extone", "source": t.TempDir(), "format": "directory"},
	}
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(format=directory) = %v, want nil", err)
	}
}

func TestPrepareFormatEmptyDefaultsToDirectory(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["extensions"] = []map[string]interface{}{
		{"name": "extone", "source": t.TempDir(), "format": ""},
	}
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(format=\"\") = %v, want nil (defaults to directory)", err)
	}
}

func TestPrepareMissingPackagingTools(t *testing.T) {
	// Format squashfs requires mksquashfs on PATH; erofs requires mkfs.erofs.
	// PATH is narrowed to an empty temp dir so exec.LookPath fails
	// deterministically regardless of what the build host has installed.
	// Tests in this package do not use t.Parallel, so PATH manipulation is
	// process-local and safe.
	tests := []struct{ format, tool string }{
		{format: "squashfs", tool: "mksquashfs"},
		{format: "erofs", tool: "mkfs.erofs"},
	}
	for _, tt := range tests {
		t.Run(tt.format, func(t *testing.T) {
			oldPATH := os.Getenv("PATH")
			if err := os.Setenv("PATH", t.TempDir()); err != nil {
				t.Fatalf("set PATH: %v", err)
			}
			defer func() { _ = os.Setenv("PATH", oldPATH) }()

			var p Provisioner
			cfg := minimalConfig(t)
			cfg["extensions"] = []map[string]interface{}{
				{"name": "extone", "source": t.TempDir(), "format": tt.format},
			}
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare(format=%s) with tool absent = nil, want error", tt.format)
			}
			msg := err.Error()
			if !strings.Contains(msg, tt.tool) {
				t.Fatalf("error %q does not name the missing tool %q", msg, tt.tool)
			}
			if !strings.Contains(msg, "extone") {
				t.Fatalf("error %q does not name the extension %q", msg, "extone")
			}
		})
	}
}

func TestPrepareInvalidReleaseFile(t *testing.T) {
	tests := []struct {
		name        string
		releaseFile string
	}{
		{name: "nonexistent", releaseFile: filepath.Join(t.TempDir(), "missing-release")},
		{name: "directory instead of file", releaseFile: t.TempDir()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["extensions"] = []map[string]interface{}{
				{"name": "extone", "source": t.TempDir(), "release_file": tt.releaseFile},
			}
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare with release_file %q = nil, want error", tt.releaseFile)
			}
			msg := err.Error()
			if !strings.Contains(msg, "release_file") {
				t.Fatalf("error %q does not name field %q", msg, "release_file")
			}
			if !strings.Contains(msg, "extone") {
				t.Fatalf("error %q does not name the extension %q", msg, "extone")
			}
		})
	}
}

func TestPrepareReleaseFileNotReadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: permission bits do not block reads, cannot exercise an unreadable release_file")
	}
	unreadable := filepath.Join(t.TempDir(), "locked")
	if err := os.WriteFile(unreadable, []byte("ID=x\nVERSION_ID=y\n"), 0o000); err != nil {
		t.Fatalf("write locked release file: %v", err)
	}
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["extensions"] = []map[string]interface{}{
		{"name": "extone", "source": t.TempDir(), "release_file": unreadable},
	}
	err := p.Prepare(cfg)
	if err == nil {
		t.Fatal("Prepare with unreadable release_file = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "release_file") {
		t.Fatalf("error %q does not name field %q", msg, "release_file")
	}
	if !strings.Contains(msg, "extone") {
		t.Fatalf("error %q does not name the extension %q", msg, "extone")
	}
}

func TestPrepareValidReleaseFile(t *testing.T) {
	release := filepath.Join(t.TempDir(), "extension-release.extone")
	if err := os.WriteFile(release, []byte("ID=ubuntu\nVERSION_ID=24.04\n"), 0o644); err != nil {
		t.Fatalf("write release file: %v", err)
	}
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["extensions"] = []map[string]interface{}{
		{"name": "extone", "source": t.TempDir(), "release_file": release},
	}
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(valid release_file) = %v, want nil", err)
	}
}

func TestPrepareRelativeGuestInstallDir(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["guest_install_dir"] = "var/lib/confexts"
	err := p.Prepare(cfg)
	if err == nil {
		t.Fatal("Prepare with relative guest_install_dir = nil, want error")
	}
	if !strings.Contains(err.Error(), "guest_install_dir") {
		t.Fatalf("error %q does not name field %q", err.Error(), "guest_install_dir")
	}
}

func TestPrepareValidGuestInstallDir(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["guest_install_dir"] = "/custom/dir"
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(valid guest_install_dir) = %v, want nil", err)
	}
	if p.config.GuestInstallDir != "/custom/dir" {
		t.Errorf("guest_install_dir = %q, want %q", p.config.GuestInstallDir, "/custom/dir")
	}
}

func TestPrepareInvalidCommand(t *testing.T) {
	// Command must be a plain executable path, no shell metacharacters.
	// Values are tested verbatim (invoked without a shell).
	for _, cmd := range []string{"foo;bar", "foo bar", "foo$(id)", "foo|bar"} {
		t.Run(cmd, func(t *testing.T) {
			var p Provisioner
			cfg := minimalConfig(t)
			cfg["command"] = cmd
			err := p.Prepare(cfg)
			if err == nil {
				t.Fatalf("Prepare with command %q = nil, want error", cmd)
			}
			if !strings.Contains(err.Error(), "command") {
				t.Fatalf("error %q does not name field %q", err.Error(), "command")
			}
		})
	}
}

func TestPrepareValidCommand(t *testing.T) {
	var p Provisioner
	cfg := minimalConfig(t)
	cfg["command"] = "/usr/bin/" + wantDefaultCommand
	if err := p.Prepare(cfg); err != nil {
		t.Fatalf("Prepare(valid command) = %v, want nil", err)
	}
}

func TestConfigSpecExposesDocumentedKeys(t *testing.T) {
	spec := (&Provisioner{}).ConfigSpec()
	required := []string{"extensions", "mode", "merge_during_build", "command", "guest_install_dir"}
	for _, key := range required {
		if _, ok := spec[key]; !ok {
			t.Errorf("ConfigSpec() missing documented key %q", key)
		}
	}
}

func TestPrepareAggregatesMultipleViolations(t *testing.T) {
	// Violations are aggregated (packersdk.MultiError); the joined
	// message names every offending field.
	var p Provisioner
	cfg := map[string]interface{}{
		"mode": "BAKE",
		"extensions": []map[string]interface{}{
			{"name": "bad name", "source": t.TempDir()},
		},
	}
	err := p.Prepare(cfg)
	if err == nil {
		t.Fatal("Prepare with two violations = nil, want error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "mode") {
		t.Errorf("aggregated error %q does not name field %q", msg, "mode")
	}
	if !strings.Contains(msg, "name") {
		t.Errorf("aggregated error %q does not name field %q", msg, "name")
	}
}
