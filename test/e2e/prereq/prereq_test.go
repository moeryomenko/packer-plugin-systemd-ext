// Contract tests for the fail-fast prerequisite checker used by the E2E
// harnesses before any cloud-hypervisor build. The harness must never report
// green without the host prerequisites, and each failure must be actionable.
//
// This file defines the public API contract for the `prereq` package. The
// exact signatures are authored here so the engineer implements to match
// (implemented in test/e2e/prereq/prereq.go):
//
//	func CheckKVM() error
//	func CheckCloudHypervisor(versionAtLeast int) error
//	func CheckPacker(versionAtLeast int) error
//	func CheckQemuImg() error
//	func CheckGo(versionAtLeast int) error
//	func CheckTapCreate() error
//	func RunAll() error
//
// plus two package-level injectable variables:
//
//	var KVMDevice   = "/dev/kvm"
//	var TapCreateFunc = func() error { ... } // real probe default
//
// Expected red state against the current tree: test/e2e/prereq contains
// only this test file, so the package does NOT compile ("no non-test Go
// files" and, once a stub package exists, "undefined: CheckKVM" etc.).
// That compile failure is the intended red phase; it is resolved when
// prereq.go is created.
//
// Design decisions (documented):
//   - KVMDevice is a package-level variable so tests can point the KVM
//     check at a nonexistent path without root or a real /dev/kvm.
//   - TapCreateFunc is a function variable so unit tests can stub the
//     privileged TAP probe. The default probe's runtime result depends on
//     uid/sudo availability, so the default is only asserted to be non-nil
//     here; its behavior is verified by the E2E sweep.
//   - CheckCloudHypervisor honours the ch_binary_path environment override
//     before falling back to $PATH (mirrors the plugin's config key).
//   - versionAtLeast encoding: CheckCloudHypervisor takes the major version
//     floor (38 means ">= v38.x"); CheckPacker and CheckGo take
//     major*100+minor (109 means ">= 1.9.x", 126 means ">= go1.26.x").
//   - Every failure error names the missing requirement:
//     kvm, cloud-hypervisor, packer, qemu-img, go, tap/root. Tests assert
//     the name appears in the error message, case-insensitively.
//
// Testability uses t.Setenv for PATH and ch_binary_path plus t.TempDir()
// for fake binaries (executable shell scripts printing a version line), so
// no host tooling or root is required.

package prereq

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	chFloor     = 38  // cloud-hypervisor major version floor
	packerFloor = 109 // packer >= 1.9 encoded as major*100+minor
	goFloor     = 126 // go >= 1.26 encoded as major*100+minor
)

// writeFake writes an executable shell script named `name` in `dir` that
// prints `stdout` and exits 0. Used to fake cloud-hypervisor, packer,
// qemu-img and go on a test-controlled PATH. Version lines in this file
// are plain (no quotes, $, or backticks), so single-quoted echo is safe.
func writeFake(t *testing.T, dir, name, stdout string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	script := "#!/bin/sh\necho '" + stdout + "'\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return p
}

// setPath restricts the process PATH to `dir` so lookups hit only fakes.
func setPath(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

// wantErrMentions fails unless err is non-nil and its message contains
// every given substring (case-insensitively).
func wantErrMentions(t *testing.T, err error, want ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error, got nil")
	}
	lower := strings.ToLower(err.Error())
	for _, w := range want {
		if !strings.Contains(lower, strings.ToLower(w)) {
			t.Errorf("error message %q does not mention %q", err, w)
		}
	}
}

func TestCheckKVMDefaultDevicePath(t *testing.T) {
	// The check verifies "/dev/kvm exists and is accessible".
	if KVMDevice != "/dev/kvm" {
		t.Errorf("KVMDevice = %q, want default %q", KVMDevice, "/dev/kvm")
	}
}

func TestCheckKVM_MissingDevice(t *testing.T) {
	old := KVMDevice
	KVMDevice = filepath.Join(t.TempDir(), "no-such-kvm")
	defer func() { KVMDevice = old }()

	wantErrMentions(t, CheckKVM(), "kvm")
}

func TestCheckKVM_ExistingDevice(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatalf("create fake kvm device: %v", err)
	}
	old := KVMDevice
	KVMDevice = dev
	defer func() { KVMDevice = old }()

	if err := CheckKVM(); err != nil {
		t.Fatalf("CheckKVM() = %v, want nil", err)
	}
}

func TestCheckKVM_EmptyPath(t *testing.T) {
	old := KVMDevice
	KVMDevice = ""
	defer func() { KVMDevice = old }()

	wantErrMentions(t, CheckKVM(), "kvm")
}

func TestCheckCloudHypervisorVersion(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		wantOK bool
	}{
		{name: "below floor", stdout: "cloud-hypervisor v37.2.0\n", wantOK: false},
		{name: "at floor", stdout: "cloud-hypervisor v38.0.0\n", wantOK: true},
		{name: "above floor", stdout: "cloud-hypervisor v38.5.0\n", wantOK: true},
		{name: "realistic suffixed at floor", stdout: "cloud-hypervisor v38.0-85-g616bafbe8\nMigration Protocol Versions: 0\n", wantOK: true},
		{name: "unparseable", stdout: "cloud-hypervisor version??\n", wantOK: false},
		{name: "empty output", stdout: "\n", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			setPath(t, dir)
			writeFake(t, dir, "cloud-hypervisor", tt.stdout)

			err := CheckCloudHypervisor(chFloor)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("CheckCloudHypervisor(%d) = %v, want nil", chFloor, err)
				}
				return
			}
			wantErrMentions(t, err, "cloud-hypervisor")
		})
	}
}

func TestCheckCloudHypervisor_EnvOverride(t *testing.T) {
	// ch_binary_path must take precedence over PATH: the fake is NOT on the
	// test PATH, only reachable through the override.
	dir := t.TempDir()
	p := writeFake(t, dir, "cloud-hypervisor-custom", "cloud-hypervisor v38.0.0\n")
	t.Setenv("ch_binary_path", p)
	setPath(t, t.TempDir())

	if err := CheckCloudHypervisor(chFloor); err != nil {
		t.Fatalf("CheckCloudHypervisor(%d) with ch_binary_path override = %v, want nil", chFloor, err)
	}
}

func TestCheckCloudHypervisor_EnvOverrideMissingPath(t *testing.T) {
	t.Setenv("ch_binary_path", filepath.Join(t.TempDir(), "does-not-exist"))
	setPath(t, t.TempDir())

	wantErrMentions(t, CheckCloudHypervisor(chFloor), "cloud-hypervisor")
}

func TestCheckCloudHypervisor_NotOnPath(t *testing.T) {
	t.Setenv("PATH", "")

	wantErrMentions(t, CheckCloudHypervisor(chFloor), "cloud-hypervisor")
}

func TestCheckPackerVersion(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		wantOK bool
	}{
		{name: "below floor", stdout: "Packer v1.8.6\n", wantOK: false},
		{name: "at floor", stdout: "Packer v1.9.0\n", wantOK: true},
		{name: "above floor", stdout: "Packer v1.11.2\n", wantOK: true},
		{name: "no v prefix", stdout: "Packer 1.10.1\n", wantOK: true},
		{name: "unparseable", stdout: "Packer unknown\n", wantOK: false},
		{name: "empty output", stdout: "\n", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			setPath(t, dir)
			writeFake(t, dir, "packer", tt.stdout)

			err := CheckPacker(packerFloor)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("CheckPacker(%d) = %v, want nil", packerFloor, err)
				}
				return
			}
			wantErrMentions(t, err, "packer")
		})
	}
}

func TestCheckPacker_NotOnPath(t *testing.T) {
	t.Setenv("PATH", "")

	wantErrMentions(t, CheckPacker(packerFloor), "packer")
}

func TestCheckGoVersion(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		wantOK bool
	}{
		{name: "below floor", stdout: "go version go1.25.4 linux/amd64\n", wantOK: false},
		{name: "at floor", stdout: "go version go1.26.0 linux/amd64\n", wantOK: true},
		{name: "above floor", stdout: "go version go1.27.0 linux/amd64\n", wantOK: true},
		{name: "task format", stdout: "go version go1.26.1 linux/amd64\n", wantOK: true},
		{name: "unparseable", stdout: "go version nope\n", wantOK: false},
		{name: "empty output", stdout: "\n", wantOK: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			setPath(t, dir)
			writeFake(t, dir, "go", tt.stdout)

			err := CheckGo(goFloor)
			if tt.wantOK {
				if err != nil {
					t.Fatalf("CheckGo(%d) = %v, want nil", goFloor, err)
				}
				return
			}
			wantErrMentions(t, err, "go")
		})
	}
}

func TestCheckGo_NotOnPath(t *testing.T) {
	t.Setenv("PATH", "")

	wantErrMentions(t, CheckGo(goFloor), "go")
}

func TestCheckQemuImg(t *testing.T) {
	t.Run("present on PATH", func(t *testing.T) {
		dir := t.TempDir()
		setPath(t, dir)
		writeFake(t, dir, "qemu-img", "qemu-img version 9.0.0\n")

		if err := CheckQemuImg(); err != nil {
			t.Fatalf("CheckQemuImg() = %v, want nil", err)
		}
	})

	t.Run("missing from PATH", func(t *testing.T) {
		t.Setenv("PATH", "")

		wantErrMentions(t, CheckQemuImg(), "qemu-img")
	})

	t.Run("present but not executable", func(t *testing.T) {
		// exec.LookPath only resolves files with an executable bit set.
		dir := t.TempDir()
		setPath(t, dir)
		if err := os.WriteFile(filepath.Join(dir, "qemu-img"), []byte("#!/bin/sh\n"), 0o644); err != nil {
			t.Fatalf("write non-executable qemu-img: %v", err)
		}

		wantErrMentions(t, CheckQemuImg(), "qemu-img")
	})
}

func TestCheckTapCreate_StubOK(t *testing.T) {
	old := TapCreateFunc
	TapCreateFunc = func() error { return nil }
	defer func() { TapCreateFunc = old }()

	if err := CheckTapCreate(); err != nil {
		t.Fatalf("CheckTapCreate() = %v, want nil", err)
	}
}

func TestCheckTapCreate_StubError(t *testing.T) {
	old := TapCreateFunc
	TapCreateFunc = func() error { return errors.New("requires root/sudo for TAP creation") }
	defer func() { TapCreateFunc = old }()

	wantErrMentions(t, CheckTapCreate(), "tap", "root")
}

func TestCheckTapCreate_HasDefaultProbe(t *testing.T) {
	// The injectable variable must default to a real probe (non-nil). Its
	// runtime result is uid/sudo-dependent and is NOT unit-tested here;
	// the E2E sweep verifies the real behavior.
	if TapCreateFunc == nil {
		t.Fatal("TapCreateFunc is nil; a default probe must be assigned in prereq.go")
	}
}

func TestRunAll_AllPass(t *testing.T) {
	dir := t.TempDir()
	setPath(t, dir)
	writeFake(t, dir, "cloud-hypervisor", "cloud-hypervisor v38.0.0\n")
	writeFake(t, dir, "packer", "Packer v1.11.2\n")
	writeFake(t, dir, "qemu-img", "qemu-img version 9.0.0\n")
	writeFake(t, dir, "go", "go version go1.26.5 linux/amd64\n")

	oldKVM := KVMDevice
	dev := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatalf("create fake kvm device: %v", err)
	}
	KVMDevice = dev
	defer func() { KVMDevice = oldKVM }()

	oldTap := TapCreateFunc
	TapCreateFunc = func() error { return nil }
	defer func() { TapCreateFunc = oldTap }()

	if err := RunAll(); err != nil {
		t.Fatalf("RunAll() = %v, want nil", err)
	}
}

func TestRunAll_KVMFirstFailFast(t *testing.T) {
	// KVM is checked first: when the device is missing, the aggregate must
	// return a kvm-naming error and never reach the later checks (the TAP
	// stub must not be invoked).
	dir := t.TempDir()
	setPath(t, dir)
	writeFake(t, dir, "cloud-hypervisor", "cloud-hypervisor v38.0.0\n")
	writeFake(t, dir, "packer", "Packer v1.11.2\n")
	writeFake(t, dir, "qemu-img", "qemu-img version 9.0.0\n")
	writeFake(t, dir, "go", "go version go1.26.5 linux/amd64\n")

	oldKVM := KVMDevice
	KVMDevice = filepath.Join(t.TempDir(), "missing-kvm")
	defer func() { KVMDevice = oldKVM }()

	tapCalled := false
	oldTap := TapCreateFunc
	TapCreateFunc = func() error { tapCalled = true; return nil }
	defer func() { TapCreateFunc = oldTap }()

	wantErrMentions(t, RunAll(), "kvm")
	if tapCalled {
		t.Error("RunAll() is not fail-fast: the TAP check ran although the KVM check already failed")
	}
}

func TestRunAll_CloudHypervisorBeforePacker(t *testing.T) {
	// Order is KVM, cloud-hypervisor, packer, qemu-img, go, tap. With KVM
	// satisfied and both cloud-hypervisor AND packer missing, the aggregate
	// must stop at the cloud-hypervisor check (second) and name it, not
	// packer.
	oldKVM := KVMDevice
	dev := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatalf("create fake kvm device: %v", err)
	}
	KVMDevice = dev
	defer func() { KVMDevice = oldKVM }()

	dir := t.TempDir()
	setPath(t, dir)
	// no cloud-hypervisor fake and no packer fake
	writeFake(t, dir, "qemu-img", "qemu-img version 9.0.0\n")
	writeFake(t, dir, "go", "go version go1.26.5 linux/amd64\n")

	oldTap := TapCreateFunc
	TapCreateFunc = func() error { return nil }
	defer func() { TapCreateFunc = oldTap }()

	err := RunAll()
	wantErrMentions(t, err, "cloud-hypervisor")
	if strings.Contains(strings.ToLower(err.Error()), "packer") {
		t.Errorf("RunAll() error %q names packer; the cloud-hypervisor check must run first", err)
	}
}

func TestRunAll_TapLast(t *testing.T) {
	// Every prerequisite except TAP is satisfied; the TAP failure (last in
	// order) must surface and name the requirement.
	dir := t.TempDir()
	setPath(t, dir)
	writeFake(t, dir, "cloud-hypervisor", "cloud-hypervisor v38.0.0\n")
	writeFake(t, dir, "packer", "Packer v1.11.2\n")
	writeFake(t, dir, "qemu-img", "qemu-img version 9.0.0\n")
	writeFake(t, dir, "go", "go version go1.26.5 linux/amd64\n")

	oldKVM := KVMDevice
	dev := filepath.Join(t.TempDir(), "kvm")
	if err := os.WriteFile(dev, nil, 0o644); err != nil {
		t.Fatalf("create fake kvm device: %v", err)
	}
	KVMDevice = dev
	defer func() { KVMDevice = oldKVM }()

	oldTap := TapCreateFunc
	TapCreateFunc = func() error { return errors.New("requires root/sudo for TAP creation") }
	defer func() { TapCreateFunc = oldTap }()

	wantErrMentions(t, RunAll(), "tap")
}
