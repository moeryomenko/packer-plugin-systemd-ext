// Test contract: verity companion integration with a real generated verity
// set.
//
// Unlike the fake-file bake tests, this file exercises the full verity path
// with a REAL generated verity set: a disk image (.raw) built from a staged
// sysext tree, a dm-verity hash tree (.verity), the root hash (.roothash),
// and a detached CMS signature of the root hash (.roothash.p7s), generated
// in-test from fresh self-signed key material.
//
// Fixture generation strategy (documented decision):
//   - The .raw image is built with mksquashfs (falling back to mkfs.erofs)
//     from a staged tree carrying usr/lib/extension-release.d/ plus a payload
//     file, mirroring the packaging layout.
//   - The verity set is produced with `veritysetup format` (the spec-approved
//     alternative to systemd-dissect --make) + `openssl smime -sign` over the
//     roothash with a fresh self-signed certificate. systemd-dissect --make
//     is NOT used because it is not available on systemd 261 (the version on
//     the authoring runner); veritysetup format is unprivileged-friendly and
//     deterministic across versions.
//   - Keys and certificates are generated ONLY inside the test temp dir
//     (t.TempDir) and never reused; no real credentials are touched.
//   - The generated set is proven real, not just present: the roothash must
//     parse as a 64-hex SHA-256 digest and the .p7s must verify against the
//     roothash via `openssl smime -verify`.
//
// Skip hygiene (integration): the fixture requires openssl, veritysetup, and
// at least one image builder (mksquashfs or mkfs.erofs). Each test skips
// cleanly with a documented reason when a tool is missing; it never
// hard-fails solely for absent tooling. Tests run under plain `go test` (no
// //go:build tag), matching package_image_test.go.
//
// The provisioner flow is driven through the fake communicator in
// comm_fake_test.go (bake mode). A .raw-only run performs no os-release
// download (release generation is not needed), so the fake serves no
// os-release bytes.
//
// Confext mirror decision: sysext only. The confext package's verity path is
// identical code (same extpkg.DetectVeritySet, same guestops placement, same
// dissect bake); its own TestBakeRawWithVerityCompanions already pins the
// confext-specific install dir /var/lib/confexts and command strings with
// fake files. A real-file confext mirror would duplicate this file with only
// those two strings differing and add no distinct coverage, so it is omitted.

package sysext

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/eryoma/packer-plugin-systemd-ext/internal/extpkg"
)

// veritySet is a generated self-signed verity set.
type veritySet struct {
	// Name is the extension/image base name, e.g. "foo".
	Name string
	// Dir is the temp dir holding the set.
	Dir string
	// Raw is <name>.raw, the disk image.
	Raw string
	// Verity is <name>.verity, the dm-verity hash tree.
	Verity string
	// RootHash is <name>.roothash, the SHA-256 root hash in hex.
	RootHash string
	// P7S is <name>.roothash.p7s, the detached CMS signature of the root hash.
	P7S string
}

// requireTool returns the absolute path of tool, skipping the test with a
// documented reason when it is not on PATH (integration hygiene: never
// hard-fail an integration test for missing environment tooling).
func requireTool(t *testing.T, tool string) string {
	t.Helper()
	p, err := exec.LookPath(tool)
	if err != nil {
		t.Skipf("integration skipped: tool %q not found on PATH (%v)", tool, err)
	}
	return p
}

// requireImageTool returns an image-builder path, skipping when neither
// mksquashfs nor mkfs.erofs is available.
func requireImageTool(t *testing.T) string {
	t.Helper()
	for _, tool := range []string{"mksquashfs", "mkfs.erofs"} {
		if p, err := exec.LookPath(tool); err == nil {
			return p
		}
	}
	t.Skipf("integration skipped: no image builder on PATH (need mksquashfs or mkfs.erofs)")
	return ""
}

// buildRawImage packages the staged tree at staged into rawPath with the
// image builder at toolPath: `mksquashfs <staged> <raw> -noappend` or
// `mkfs.erofs <raw> <staged>` (the same invocations as the image packager).
func buildRawImage(t *testing.T, toolPath, staged, rawPath string) {
	t.Helper()
	tool := filepath.Base(toolPath)
	var args []string
	switch tool {
	case "mksquashfs":
		args = []string{staged, rawPath, "-noappend"}
	case "mkfs.erofs":
		args = []string{rawPath, staged}
	default:
		t.Fatalf("buildRawImage: unsupported image builder %q", tool)
	}
	cmd := exec.Command(toolPath, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("%s failed: %v\n%s", tool, err, out)
	}
}

// parseRootHash extracts the root hash from `veritysetup format` output.
// The output line is "Root hash:\t<64-hex>".
var rootHashRe = regexp.MustCompile(`Root hash:[[:space:]]+([0-9a-fA-F]{64})`)

// generateVeritySet builds a real self-signed verity set for name inside dir:
//
//	staged/usr/lib/extension-release.d/extension-release.<name>  (sysext layout)
//	staged/usr/lib/<name>-payload
//	<name>.raw            via mksquashfs / mkfs.erofs
//	<name>.verity         via veritysetup format
//	<name>.roothash       the root hash printed by veritysetup
//	<name>.roothash.p7s   detached CMS signature via openssl smime -sign
//
// All key material is created under dir (the caller passes t.TempDir()); never
// any real credentials. The set is then proven real: the roothash is a 64-hex
// SHA-256 digest and the .p7s verifies against the roothash.
func generateVeritySet(t *testing.T, dir, name string) veritySet {
	t.Helper()
	openssl := requireTool(t, "openssl")
	veritysetup := requireTool(t, "veritysetup")
	imageTool := requireImageTool(t)

	// Stage a minimal sysext tree (packaging layout): release file + payload.
	staged := filepath.Join(dir, "staged")
	releaseDir := filepath.Join(staged, "usr", "lib", "extension-release.d")
	if err := os.MkdirAll(releaseDir, 0o755); err != nil {
		t.Fatalf("mkdir release dir: %v", err)
	}
	releasePath := filepath.Join(releaseDir, "extension-release."+name)
	if err := os.WriteFile(releasePath, []byte("ID=arch\nVERSION_ID=1\n"), 0o644); err != nil {
		t.Fatalf("write release file: %v", err)
	}
	payload := filepath.Join(staged, "usr", "lib", name+"-payload")
	if err := os.WriteFile(payload, []byte("verity integration payload\n"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}

	// The disk image.
	raw := filepath.Join(dir, name+".raw")
	buildRawImage(t, imageTool, staged, raw)

	// The dm-verity hash tree + root hash.
	verity := filepath.Join(dir, name+".verity")
	cmd := exec.Command(veritysetup, "format", "--data-block-size", "4096",
		"--hash-block-size", "4096", raw, verity)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("veritysetup format failed: %v\n%s", err, out)
	}
	m := rootHashRe.FindStringSubmatch(string(out))
	if m == nil {
		t.Fatalf("veritysetup format output has no SHA-256 root hash:\n%s", out)
	}
	roothash := filepath.Join(dir, name+".roothash")
	if err := os.WriteFile(roothash, []byte(strings.ToLower(m[1])+"\n"), 0o644); err != nil {
		t.Fatalf("write roothash: %v", err)
	}

	// Fresh self-signed key + cert, then the detached signature.
	keyPath := filepath.Join(dir, "key.pem")
	certPath := filepath.Join(dir, "cert.pem")
	req := exec.Command(openssl, "req", "-x509", "-newkey", "rsa:2048",
		"-keyout", keyPath, "-out", certPath, "-days", "1", "-nodes",
		"-subj", "/CN=packer-systemd-ext-test")
	if out, err := req.CombinedOutput(); err != nil {
		t.Fatalf("openssl req failed: %v\n%s", err, out)
	}
	p7s := filepath.Join(dir, name+".roothash.p7s")
	sign := exec.Command(openssl, "smime", "-sign", "-binary",
		"-in", roothash, "-inkey", keyPath, "-signer", certPath,
		"-out", p7s, "-outform", "DER")
	if out, err := sign.CombinedOutput(); err != nil {
		t.Fatalf("openssl smime -sign failed: %v\n%s", err, out)
	}

	set := veritySet{Name: name, Dir: dir, Raw: raw, Verity: verity, RootHash: roothash, P7S: p7s}
	verifySelfSignedSet(t, set)
	return set
}

// verifySelfSignedSet proves the generated set is real: the roothash is a
// 64-hex SHA-256 digest and the .p7s is a valid detached signature over it.
func verifySelfSignedSet(t *testing.T, set veritySet) {
	t.Helper()
	openssl := requireTool(t, "openssl")

	rh, err := os.ReadFile(set.RootHash)
	if err != nil {
		t.Fatalf("read roothash %s: %v", set.RootHash, err)
	}
	if !regexp.MustCompile(`^[0-9a-fA-F]{64}\n$`).Match(rh) {
		t.Fatalf("roothash %q is not a 64-hex SHA-256 digest", rh)
	}
	cmd := exec.Command(openssl, "smime", "-verify", "-binary",
		"-in", set.P7S, "-content", set.RootHash, "-inform", "DER", "-noverify")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("roothash signature does not verify: %v\n%s", err, out)
	}
}

// veritySourceConfig returns a bake config with one prebuilt .raw source
// pointing at set.Raw.
func veritySourceConfig(t *testing.T, set veritySet) map[string]interface{} {
	t.Helper()
	return map[string]interface{}{
		"extensions": []map[string]interface{}{
			{"name": set.Name, "source": set.Raw},
		},
	}
}

func TestVerityIntegrationGeneratedSetIsSelfSigned(t *testing.T) {
	// The fixture generator produces a REAL four-file verity set (raw,
	// verity, roothash, roothash.p7s), and the signature proves the
	// roothash is genuinely signed (self-signed in-test, no real credentials).
	// Skips cleanly when openssl/veritysetup/an image builder is absent.
	set := generateVeritySet(t, t.TempDir(), "foo")
	for _, f := range []string{set.Raw, set.Verity, set.RootHash, set.P7S} {
		fi, err := os.Stat(f)
		if err != nil {
			t.Fatalf("generated file %s missing: %v", f, err)
		}
		if fi.Size() == 0 {
			t.Fatalf("generated file %s is empty", f)
		}
	}
}

func TestVerityIntegrationDetectFullSet(t *testing.T) {
	// DetectVeritySet returns the full companion set for the generated files
	// — .verity, .roothash, .roothash.p7s in the pinned upload order. The raw
	// plus these three companions is the four-file set.
	set := generateVeritySet(t, t.TempDir(), "foo")

	got, err := extpkg.DetectVeritySet(set.Raw)
	if err != nil {
		t.Fatalf("DetectVeritySet(%s): unexpected error %v", set.Raw, err)
	}
	want := []string{set.Verity, set.RootHash, set.P7S}
	if len(got) != len(want) {
		t.Fatalf("DetectVeritySet(%s) returned %d companions, want %d: %q",
			set.Raw, len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("DetectVeritySet(%s) companion %d = %q, want %q", set.Raw, i, got[i], want[i])
		}
	}
}

func TestVerityIntegrationBakeUploadsFullSet(t *testing.T) {
	// Driving the provisioner bake flow with the generated .raw source
	// uploads all four artifacts (raw + verity + roothash + p7s), and the
	// command stream contains the systemd-dissect --copy-from for the .raw. A
	// .raw-only run performs no os-release read.
	set := generateVeritySet(t, t.TempDir(), "foo")

	var p Provisioner
	if err := p.Prepare(veritySourceConfig(t, set)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("", rawBakeResponses("systemd-sysext", "No extensions applied.\n")...)
	if err := provisionBake(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertUploads(t, comm,
		"/var/tmp/packer-sysext-RANDOM/foo.raw",
		"/var/tmp/packer-sysext-RANDOM/foo.verity",
		"/var/tmp/packer-sysext-RANDOM/foo.roothash",
		"/var/tmp/packer-sysext-RANDOM/foo.roothash.p7s",
	)
	assertUploadDirs(t, comm) // .raw format uploads files only
	assertDownloads(t, comm)  // no directory source -> no os-release read

	assertCommands(t, comm, []string{
		"command -v systemd-sysext",
		"systemctl --version",
		"id -u",
		"mkdir -p /var/tmp/packer-sysext-RANDOM",
		"mv /var/tmp/packer-sysext-RANDOM/foo.raw /var/lib/extensions/foo.raw.tmpRANDOM",
		"mv /var/lib/extensions/foo.raw.tmpRANDOM /var/lib/extensions/foo.raw",
		"chown root:root /var/lib/extensions/foo.raw",
		"mv /var/tmp/packer-sysext-RANDOM/foo.verity /var/lib/extensions/foo.verity.tmpRANDOM",
		"mv /var/lib/extensions/foo.verity.tmpRANDOM /var/lib/extensions/foo.verity",
		"chown root:root /var/lib/extensions/foo.verity",
		"mv /var/tmp/packer-sysext-RANDOM/foo.roothash /var/lib/extensions/foo.roothash.tmpRANDOM",
		"mv /var/lib/extensions/foo.roothash.tmpRANDOM /var/lib/extensions/foo.roothash",
		"chown root:root /var/lib/extensions/foo.roothash",
		"mv /var/tmp/packer-sysext-RANDOM/foo.roothash.p7s /var/lib/extensions/foo.roothash.p7s.tmpRANDOM",
		"mv /var/lib/extensions/foo.roothash.p7s.tmpRANDOM /var/lib/extensions/foo.roothash.p7s",
		"chown root:root /var/lib/extensions/foo.roothash.p7s",
		"systemd-sysext status",
		"systemd-sysext merge",
		"systemd-sysext unmerge",
		"systemd-dissect --copy-from /var/lib/extensions/foo.raw / /var/tmp/packer-bake-foo/",
		"cp -a /var/tmp/packer-bake-foo/. /",
		"rm -rf /var/tmp/packer-bake-foo",
		"rm -rf /var/lib/extensions/foo.raw /var/lib/extensions/foo.verity /var/lib/extensions/foo.roothash /var/lib/extensions/foo.roothash.p7s",
		"rm -f /usr/lib/extension-release.d/extension-release.foo",
	})
	assertNoEnableCommands(t, comm.commands)
}

func TestVerityIntegrationMissingCompanionFailsBeforeMerge(t *testing.T) {
	// With one companion deleted, the run fails with "INCOMPLETE_VERITY_SET"
	// naming the missing file, detected deterministically
	// BEFORE any placement or merge: the stream contains only the preflight
	// commands and no upload happens.
	set := generateVeritySet(t, t.TempDir(), "foo")
	if err := os.Remove(set.RootHash); err != nil {
		t.Fatalf("remove roothash %s: %v", set.RootHash, err)
	}

	var p Provisioner
	if err := p.Prepare(veritySourceConfig(t, set)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	comm := newBakeComm("", preflightResponses("systemd-sysext")...)
	err := provisionBake(t, &p, testUi(), comm)
	if err == nil {
		t.Fatal("Provision returned nil error, want INCOMPLETE_VERITY_SET")
	}
	for _, want := range []string{"INCOMPLETE_VERITY_SET", "foo.roothash"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q (must name the missing file)", err.Error(), want)
		}
	}

	// Deterministic: the missing companion is detected before any merge, and
	// before even any upload/placement (fail-fast).
	assertCommands(t, comm, []string{
		"command -v systemd-sysext",
		"systemctl --version",
		"id -u",
	})
	assertUploads(t, comm)
	assertUploadDirs(t, comm)
	assertDownloads(t, comm)
}

func TestVerityIntegrationBareRawUploadsNoCompanions(t *testing.T) {
	// Edge: a prebuilt .raw with no companions is valid; only the .raw is
	// uploaded (no companion uploads) and the dissect bake proceeds. This
	// deliberately skips veritysetup/openssl — a bare image needs only an
	// image builder.
	dir := t.TempDir()
	staged := filepath.Join(dir, "staged")
	if err := os.MkdirAll(filepath.Join(staged, "usr", "lib"), 0o755); err != nil {
		t.Fatalf("mkdir staged tree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(staged, "usr", "lib", "payload"), []byte("bare raw payload\n"), 0o644); err != nil {
		t.Fatalf("write payload: %v", err)
	}
	raw := filepath.Join(dir, "foo.raw")
	buildRawImage(t, requireImageTool(t), staged, raw)

	set := veritySet{Name: "foo", Dir: dir, Raw: raw}

	var p Provisioner
	if err := p.Prepare(veritySourceConfig(t, set)); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	// Script: preflight (3) + placement for ONE artifact (3) + status, merge,
	// unmerge, dissect, cp, rm tmp, rm artifact, rm release.
	resp := preflightResponses("systemd-sysext")
	resp = append(resp, placementResponses(1)...)
	resp = append(resp,
		scriptedResponse{stdout: "No extensions applied.\n"}, // status
		scriptedResponse{}, // merge
		scriptedResponse{}, // unmerge
		scriptedResponse{}, // systemd-dissect --copy-from
		scriptedResponse{}, // cp -a
		scriptedResponse{}, // rm -rf tmp bake dir
		scriptedResponse{}, // rm -rf artifact
		scriptedResponse{}, // rm -f release
	)
	comm := newBakeComm("", resp...)
	if err := provisionBake(t, &p, testUi(), comm); err != nil {
		t.Fatalf("Provision: %v", err)
	}

	assertUploads(t, comm, "/var/tmp/packer-sysext-RANDOM/foo.raw")
	assertUploadDirs(t, comm)
	assertDownloads(t, comm)

	cmds := normalizeStream(comm.commands)
	foundDissect := false
	for _, c := range cmds {
		if strings.Contains(c, "systemd-dissect --copy-from /var/lib/extensions/foo.raw") {
			foundDissect = true
		}
		if strings.Contains(c, "foo.verity") || strings.Contains(c, "foo.roothash") {
			t.Errorf("bare .raw run issued a companion command: %q", c)
		}
	}
	if !foundDissect {
		t.Errorf("bare .raw run has no systemd-dissect --copy-from for foo.raw; stream:\n  %s", strings.Join(cmds, "\n  "))
	}
}
