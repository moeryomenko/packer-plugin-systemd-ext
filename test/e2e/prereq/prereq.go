// Package prereq implements the fail-fast host-prerequisite checks that the
// E2E harnesses run before any cloud-hypervisor build. Each check returns an
// actionable error naming the missing requirement; RunAll runs the checks in
// the required order and returns the
// first failure so a host without e.g. /dev/kvm is stopped before any build.
//
// The checks are unit-testable without host tooling: KVMDevice and
// TapCreateFunc are injectable package variables, and binary resolution
// honours the process PATH (plus the ch_binary_path environment override for
// cloud-hypervisor, mirroring the plugin's config key).
package prereq

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
)

// Version floors for the aggregate checks. CheckCloudHypervisor
// compares the major version directly; CheckPacker and CheckGo compare
// major*100+minor (109 means ">= 1.9.x", 126 means ">= go1.26.x").
const (
	chMajorFloor       = 38  // cloud-hypervisor >= v38
	packerVersionFloor = 109 // packer >= 1.9
	goVersionFloor     = 126 // go >= 1.26
)

// KVMDevice is the KVM device node checked by CheckKVM. It is a package
// variable so tests can point the check at a nonexistent or empty path
// without root privileges.
var KVMDevice = "/dev/kvm"

// TapCreateFunc probes whether the privileged TAP device creation
// (`ip tuntap add dev ch-tap-0 mode tap`) can succeed on this host. It is a
// package variable so unit tests can stub it. The default probe requires
// root or passwordless sudo and does not mutate the host.
var TapCreateFunc = func() error {
	if os.Getuid() == 0 {
		return nil
	}
	if err := exec.Command("sudo", "-n", "true").Run(); err == nil {
		return nil
	}
	return errors.New("requires root/sudo for TAP creation")
}

// versionRE matches the first MAJOR[.MINOR[.PATCH]] number sequence in a
// tool's version output, e.g. "38.5" in "cloud-hypervisor v38.5.0",
// "1.11" in "Packer v1.11.2", or "1.26" in "go version go1.26.1 linux/amd64".
var versionRE = regexp.MustCompile(`(\d+)(?:\.(\d+))?(?:\.(\d+))?`)

// parseVersion extracts the major and minor components of the first version
// number found in out. ok is false when out contains no version number.
func parseVersion(out string) (major, minor int, ok bool) {
	m := versionRE.FindStringSubmatch(out)
	if m == nil {
		return 0, 0, false
	}
	major, _ = strconv.Atoi(m[1])
	if m[2] != "" {
		minor, _ = strconv.Atoi(m[2])
	}
	return major, minor, true
}

// CheckKVM verifies that the KVM device node exists and is accessible.
func CheckKVM() error {
	if KVMDevice == "" {
		return errors.New("KVM device path is empty; /dev/kvm is required")
	}
	f, err := os.Open(KVMDevice)
	if err != nil {
		return fmt.Errorf("KVM device %s is not accessible: %w", KVMDevice, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("KVM device %s: closing probe handle: %w", KVMDevice, err)
	}
	return nil
}

// CheckCloudHypervisor verifies that a cloud-hypervisor binary resolves and
// that its major version is at least versionAtLeast. The ch_binary_path
// environment variable takes precedence over the PATH lookup.
func CheckCloudHypervisor(versionAtLeast int) error {
	binary := os.Getenv("ch_binary_path")
	if binary == "" {
		var err error
		binary, err = exec.LookPath("cloud-hypervisor")
		if err != nil {
			return fmt.Errorf("cloud-hypervisor not found on PATH: %w", err)
		}
	}
	if _, err := os.Stat(binary); err != nil {
		return fmt.Errorf("cloud-hypervisor (ch_binary_path %s) not found: %w", binary, err)
	}
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("cloud-hypervisor --version failed: %w", err)
	}
	major, _, ok := parseVersion(string(out))
	if !ok {
		return fmt.Errorf("cloud-hypervisor --version output %q is unparseable", string(out))
	}
	if major < versionAtLeast {
		return fmt.Errorf("cloud-hypervisor version %d is below required %d", major, versionAtLeast)
	}
	return nil
}

// CheckPacker verifies that packer resolves on PATH and that its version,
// encoded as major*100+minor (109 means ">= 1.9.x"), is at least
// versionAtLeast.
func CheckPacker(versionAtLeast int) error {
	binary, err := exec.LookPath("packer")
	if err != nil {
		return fmt.Errorf("packer not found on PATH: %w", err)
	}
	out, err := exec.Command(binary, "--version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("packer --version failed: %w", err)
	}
	major, minor, ok := parseVersion(string(out))
	if !ok {
		return fmt.Errorf("packer --version output %q is unparseable", string(out))
	}
	if major*100+minor < versionAtLeast {
		return fmt.Errorf("packer version %d.%d is below required %d", major, minor, versionAtLeast)
	}
	return nil
}

// CheckQemuImg verifies that qemu-img resolves on PATH. It is required for
// the qcow2 -> raw conversion in the harness setup.
func CheckQemuImg() error {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		return fmt.Errorf("qemu-img not found on PATH: %w", err)
	}
	return nil
}

// CheckGo verifies that the go toolchain resolves on PATH and that its
// version, encoded as major*100+minor (126 means ">= go1.26.x"), is at
// least versionAtLeast. The go toolchain builds the cloud-hypervisor plugin
// from source in the harness setup.
func CheckGo(versionAtLeast int) error {
	binary, err := exec.LookPath("go")
	if err != nil {
		return fmt.Errorf("go not found on PATH: %w", err)
	}
	out, err := exec.Command(binary, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("go version failed: %w", err)
	}
	major, minor, ok := parseVersion(string(out))
	if !ok {
		return fmt.Errorf("go version output %q is unparseable", string(out))
	}
	if major*100+minor < versionAtLeast {
		return fmt.Errorf("go version %d.%d is below required %d", major, minor, versionAtLeast)
	}
	return nil
}

// CheckTapCreate verifies that the TAP device creation probe succeeds. TAP
// creation (`ip tuntap add`) requires root or sudo, so a failure here names
// the tap/root requirement.
func CheckTapCreate() error {
	if TapCreateFunc == nil {
		return errors.New("TAP creation check unavailable: requires root/sudo for TAP creation")
	}
	if err := TapCreateFunc(); err != nil {
		return fmt.Errorf("TAP creation requires root/sudo: %w", err)
	}
	return nil
}

// RunAll runs the checks in the specified order — KVM,
// cloud-hypervisor (>= 38), packer (>= 1.9), qemu-img, go (>= 1.26),
// TAP/root — and returns the first failure with an actionable message naming
// the missing requirement.
func RunAll() error {
	checks := []struct {
		name string
		run  func() error
	}{
		{name: "kvm", run: CheckKVM},
		{name: "cloud-hypervisor", run: func() error { return CheckCloudHypervisor(chMajorFloor) }},
		{name: "packer", run: func() error { return CheckPacker(packerVersionFloor) }},
		{name: "qemu-img", run: CheckQemuImg},
		{name: "go", run: func() error { return CheckGo(goVersionFloor) }},
		{name: "tap/root", run: CheckTapCreate},
	}
	for _, c := range checks {
		if err := c.run(); err != nil {
			return fmt.Errorf("%s check failed: %w", c.name, err)
		}
	}
	return nil
}
