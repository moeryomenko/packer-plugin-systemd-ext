// Shared test double: a scripted fake packersdk.Communicator for bake-flow
// tests.
//
// Design decision (documented):
// the fake lives as a _test.go file in the provisioner package (one per
// package: provisioner/sysext/comm_fake_test.go and
// provisioner/confext/comm_fake_test.go) rather than a shared
// provisioner/testcomm package. A non-test package would compile test-only
// helpers into `go build ./...`; _test.go files stay out of the module
// build and keep the two provisioner packages independent. The duplication
// (~80 lines) is the cost of that isolation.
//
// The fake records, in call order:
//   - every Start command (rc.Command)
//   - every Upload target path
//   - every UploadDir destination
//   - every Download target path
//
// Start is answered from a FIFO response table; an unscripted Start call is
// recorded and flagged (the assertions fail) but still exits so the caller
// never hangs. Download serves the canned osRelease bytes, which is how the
// provisioner reads the guest's /etc/os-release. Upload and
// UploadDir never fail; their recorded destinations are the assertions.
//
// The base *packersdk.MockCommunicator supplies the remaining interface
// methods (DownloadDir), so bakeComm is a full packersdk.Communicator.
//
// The persist-flow tests (persist flow + idempotency) reuse this fake: it
// already records enable/is-enabled commands, which the persist tests assert
// on.

package sysext

import (
	"context"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"testing"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"

	guestops "github.com/eryoma/packer-plugin-systemd-ext/internal/guestops"
)

// scriptedResponse is the canned stdout/stderr/exit for one Start call.
type scriptedResponse struct {
	stdout string
	stderr string
	exit   int
}

// bakeComm is the scripted fake communicator described in the file header.
type bakeComm struct {
	*packersdk.MockCommunicator

	mu         sync.Mutex
	responses  []scriptedResponse
	commands   []string
	uploads    []string
	uploadDirs []string
	downloads  []string
	osRelease  string
	extra      bool
}

var _ packersdk.Communicator = (*bakeComm)(nil)

// newBakeComm builds a fake communicator. osRelease is the content served
// by Download("/etc/os-release"); responses are the FIFO Start script.
func newBakeComm(osRelease string, responses ...scriptedResponse) *bakeComm {
	return &bakeComm{
		MockCommunicator: new(packersdk.MockCommunicator),
		responses:        responses,
		osRelease:        osRelease,
	}
}

func (c *bakeComm) Start(_ context.Context, rc *packersdk.RemoteCmd) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.commands = append(c.commands, rc.Command)

	resp := scriptedResponse{exit: 0}
	if len(c.responses) > 0 {
		resp = c.responses[0]
		c.responses = c.responses[1:]
	} else {
		c.extra = true
	}

	go func() {
		if rc.Stdout != nil && resp.stdout != "" {
			_, _ = io.Copy(rc.Stdout, strings.NewReader(resp.stdout))
		}
		if rc.Stderr != nil && resp.stderr != "" {
			_, _ = io.Copy(rc.Stderr, strings.NewReader(resp.stderr))
		}
		rc.SetExited(resp.exit)
	}()
	return nil
}

func (c *bakeComm) Upload(path string, r io.Reader, fi *os.FileInfo) error {
	c.mu.Lock()
	c.uploads = append(c.uploads, path)
	c.mu.Unlock()
	return c.MockCommunicator.Upload(path, r, fi)
}

func (c *bakeComm) UploadDir(dst string, src string, excl []string) error {
	c.mu.Lock()
	c.uploadDirs = append(c.uploadDirs, dst)
	c.mu.Unlock()
	return c.MockCommunicator.UploadDir(dst, src, excl)
}

func (c *bakeComm) Download(path string, w io.Writer) error {
	c.mu.Lock()
	c.downloads = append(c.downloads, path)
	c.mu.Unlock()
	if c.osRelease != "" {
		_, _ = w.Write([]byte(c.osRelease))
	}
	return nil
}

// ---------------------------------------------------------------------------
// Random-token normalization
// ---------------------------------------------------------------------------

var (
	// stagingRe matches the random upload staging dir token, e.g.
	// packer-sysext-9f2a (/var/tmp/packer-<type>-<random>/).
	stagingRe = regexp.MustCompile(`packer-(sysext|confext)-[A-Za-z0-9]+`)
	// tmpSuffixRe matches the random atomic-placement temp-sibling suffix,
	// e.g. extone.tmp9f2a (stage under a sibling temp name).
	tmpSuffixRe = regexp.MustCompile(`\.tmp[A-Za-z0-9]+`)
)

// normalizeRandom replaces the per-run random tokens in a command line with
// the sentinel "RANDOM" so the pinned want stream and any actual run compare
// exactly. Applying it to the want stream is idempotent (RANDOM is itself
// alphanumeric).
func normalizeRandom(s string) string {
	s = stagingRe.ReplaceAllString(s, "packer-$1-RANDOM")
	s = tmpSuffixRe.ReplaceAllString(s, ".tmpRANDOM")
	return s
}

func normalizeStream(cmds []string) []string {
	out := make([]string, len(cmds))
	for i, c := range cmds {
		out[i] = normalizeRandom(c)
	}
	return out
}

// ---------------------------------------------------------------------------
// Assertion helpers
// ---------------------------------------------------------------------------

// assertCommands asserts the exact ordered Start stream, after random-token
// normalization. Any unscripted Start call fails the test too.
func assertCommands(t *testing.T, comm *bakeComm, want []string) {
	t.Helper()
	if comm.extra {
		t.Error("fake communicator received more Start calls than scripted responses; the provisioner issued an unexpected command")
	}
	got := normalizeStream(comm.commands)
	if len(got) != len(want) {
		t.Errorf("issued %d commands, want %d:\n got:\n  %s\nwant:\n  %s",
			len(got), len(want), strings.Join(got, "\n  "), strings.Join(want, "\n  "))
		return
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("command %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// assertUploads asserts the exact ordered Upload target paths (normalized).
func assertUploads(t *testing.T, comm *bakeComm, want ...string) {
	t.Helper()
	if len(comm.uploads) != len(want) {
		t.Errorf("issued %d Upload calls, want %d: got %q, want %q", len(comm.uploads), len(want), comm.uploads, want)
		return
	}
	for i := range want {
		if normalizeRandom(comm.uploads[i]) != want[i] {
			t.Errorf("Upload %d = %q, want %q", i, comm.uploads[i], want[i])
		}
	}
}

// assertUploadDirs asserts the exact ordered UploadDir destinations
// (normalized).
func assertUploadDirs(t *testing.T, comm *bakeComm, want ...string) {
	t.Helper()
	if len(comm.uploadDirs) != len(want) {
		t.Errorf("issued %d UploadDir calls, want %d: got %q, want %q", len(comm.uploadDirs), len(want), comm.uploadDirs, want)
		return
	}
	for i := range want {
		if normalizeRandom(comm.uploadDirs[i]) != want[i] {
			t.Errorf("UploadDir %d = %q, want %q", i, comm.uploadDirs[i], want[i])
		}
	}
}

// assertDownloads asserts the exact ordered Download target paths.
func assertDownloads(t *testing.T, comm *bakeComm, want ...string) {
	t.Helper()
	if len(comm.downloads) != len(want) {
		t.Errorf("issued %d Download calls, want %d: got %q, want %q", len(comm.downloads), len(want), comm.downloads, want)
		return
	}
	for i := range want {
		if comm.downloads[i] != want[i] {
			t.Errorf("Download %d = %q, want %q", i, comm.downloads[i], want[i])
		}
	}
}

// assertNoEnableCommands fails if any command contains the boot-service
// verbs enable/is-enabled. Bake mode must not issue boot-service commands.
func assertNoEnableCommands(t *testing.T, cmds []string) {
	t.Helper()
	for _, c := range cmds {
		for _, tok := range strings.Fields(c) {
			if tok == "enable" || tok == "is-enabled" {
				t.Errorf("bake mode issued boot-service command %q", c)
			}
		}
	}
}

// testUi returns a UI that discards output; capturedUi returns a UI that
// records Say/Error output for warning assertions.
func testUi() packersdk.Ui {
	return &packersdk.BasicUi{
		Reader:      strings.NewReader(""),
		Writer:      io.Discard,
		ErrorWriter: io.Discard,
	}
}

func capturedUi() (packersdk.Ui, *strings.Builder) {
	var sb strings.Builder
	return &packersdk.BasicUi{
		Reader:      strings.NewReader(""),
		Writer:      &sb,
		ErrorWriter: &sb,
	}, &sb
}

// provisionBake runs Provision with the given UI and fake communicator.
func provisionBake(t *testing.T, p *Provisioner, ui packersdk.Ui, comm *bakeComm) error {
	t.Helper()
	return p.Provision(t.Context(), ui, comm, nil)
}

// requireGuestError extracts the typed *guestops.Error from err via
// errors.As (typed runtime errors).
func requireGuestError(t *testing.T, err error) *guestops.Error {
	t.Helper()
	if err == nil {
		t.Fatal("Provision returned nil error, want typed error")
	}
	var ge *guestops.Error
	if !errors.As(err, &ge) {
		t.Fatalf("error %q is not (or does not wrap) a *guestops.Error", err)
	}
	return ge
}

// ---------------------------------------------------------------------------
// Scripted response builders
// ---------------------------------------------------------------------------

// preflightResponses scripts the preflight: command -v, systemctl --version,
// id -u. command is the provisioner's configured command.
func preflightResponses(command string) []scriptedResponse {
	return []scriptedResponse{
		{stdout: "/usr/bin/" + command + "\n"},     // command -v
		{stdout: "systemd 255 (255.4-1ubuntu8)\n"}, // systemctl --version (>= 252 floor)
		{stdout: "0\n"}, // id -u
	}
}

// placementResponses scripts the placement flow for nArtifacts: first the
// staging-root mkdir (issued once before the first upload), then per artifact
// mv staging->temp-sibling, mv temp-sibling->final, chown. All exit 0.
func placementResponses(nArtifacts int) []scriptedResponse {
	resp := make([]scriptedResponse, 0, 1+3*nArtifacts)
	resp = append(resp, scriptedResponse{}) // mkdir -p staging root
	for i := 0; i < nArtifacts; i++ {
		resp = append(resp, scriptedResponse{}, scriptedResponse{}, scriptedResponse{})
	}
	return resp
}

// bakeDirectoryResponses scripts a single-extension directory-format bake
// run: preflight, placement (mv/mv/chown), merge-state status, merge, and —
// on merge success — unmerge, cp -a, artifact removal, release removal.
// statusStdout is the merge-state check output; mergeExit/mergeStderr script
// the merge command. When mergeExit != 0 the script ends with the post-merge
// status capture and no continuation commands.
func bakeDirectoryResponses(command, statusStdout string, mergeExit int, mergeStderr string) []scriptedResponse {
	resp := preflightResponses(command)
	resp = append(resp, placementResponses(1)...) // mv staging->temp, mv temp->final, chown
	resp = append(resp, scriptedResponse{stdout: statusStdout})
	resp = append(resp, scriptedResponse{stderr: mergeStderr, exit: mergeExit})
	if mergeExit != 0 {
		// On non-zero merge exit the provisioner captures status output
		// before failing with MERGE_FAILED.
		resp = append(resp, scriptedResponse{stdout: "extension is merged\n"})
		return resp
	}
	resp = append(resp,
		scriptedResponse{}, // unmerge
		scriptedResponse{}, // cp -a <install_dir>/<name>/. /
		scriptedResponse{}, // rm -rf extension artifacts
		scriptedResponse{}, // rm -f baked release file
	)
	return resp
}

// alreadyMergedResponses scripts a directory-format bake run whose
// merge-state status reports the hierarchy already merged (stdout contains
// "merged"), so the provisioner must unmerge first with a UI warning and then
// run the normal bake sequence.
func alreadyMergedResponses(command string) []scriptedResponse {
	resp := preflightResponses(command)
	resp = append(resp, placementResponses(1)...)
	resp = append(resp, scriptedResponse{stdout: "extone is merged\n"}) // status: already merged
	resp = append(resp,
		scriptedResponse{}, // pre-merge unmerge
		scriptedResponse{}, // merge
		scriptedResponse{}, // unmerge
		scriptedResponse{}, // cp -a
		scriptedResponse{}, // rm -rf
		scriptedResponse{}, // rm -f
	)
	return resp
}

// rawBakeResponses scripts a single .raw-format bake run with the full
// verity companion set (raw, verity, roothash, roothash.p7s): preflight,
// placement for all four artifacts, merge-state status, merge, unmerge, the
// systemd-dissect copy, rootfs copy, temp-dir removal, artifact removal, and
// release removal.
func rawBakeResponses(command, statusStdout string) []scriptedResponse {
	resp := preflightResponses(command)
	resp = append(resp, placementResponses(4)...) // 4 artifacts x (mv, mv, chown)
	resp = append(resp, scriptedResponse{stdout: statusStdout})
	resp = append(resp,
		scriptedResponse{}, // merge
		scriptedResponse{}, // unmerge
		scriptedResponse{}, // systemd-dissect --copy-from
		scriptedResponse{}, // cp -a
		scriptedResponse{}, // rm -rf tmp bake dir
		scriptedResponse{}, // rm -rf artifacts
		scriptedResponse{}, // rm -f release
	)
	return resp
}
