package guestops

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
)

// CodeUploadFailed reports a failure to upload an artifact to the guest or to
// atomically place it into the install directory.
const CodeUploadFailed Code = "UPLOAD_FAILED"

// CodeDownloadFailed reports a failure to read a file from the guest, e.g.
// the /etc/os-release read.
const CodeDownloadFailed Code = "DOWNLOAD_FAILED"

// Artifact is one uploadable artifact of an extension: the staged tree of a
// directory source or one file of a prebuilt .raw set.
type Artifact struct {
	// Basename is the artifact's name inside the staging root and inside the
	// install directory: the extension name for directory format, the file
	// basename (e.g. rawimg.raw, rawimg.verity) for a .raw set.
	Basename string
	// LocalPath is the local directory or file to upload.
	LocalPath string
	// Directory marks a directory artifact: it is uploaded with UploadDir and
	// normalized with `chown -R root:root`.
	Directory bool
}

// UploadAndPlace uploads artifacts to the private guest staging path
// /var/tmp/packer-<type>-<random>/ and atomically places each
// into installDir:
//
//	mkdir -p /var/tmp/packer-<type>-<token>
//	upload staging/<basename>
//	mv staging/<basename> installDir/<basename>.tmp<token>
//	mv installDir/<basename>.tmp<token> installDir/<basename>
//	chown [-R] root:root installDir/<basename>
//
// The staging root is created on the guest before the first upload because
// the scp-backed UploadDir/Upload calls fail ("No such file or directory")
// when the target directory does not exist. The final mv is a plain POSIX mv,
// which overwrites an existing destination (the overwrite semantics pinned
// by the bake tests). All commands are issued as plain argv, never
// through a shell. On success it returns the random token used for the
// staging path and the temp-sibling suffix. On any failure it returns a
// *Error with code UPLOAD_FAILED naming the failing operation.
func UploadAndPlace(ctx context.Context, comm packersdk.Communicator, typ ExtensionType, installDir string, artifacts []Artifact) (string, error) {
	token := randomToken()
	stagingRoot := fmt.Sprintf("/var/tmp/packer-%s-%s", typ, token)
	// The private staging path must exist before any upload lands in it.
	// mkdir -p is idempotent on re-runs.
	if _, _, err := run(ctx, comm, []string{"mkdir", "-p", stagingRoot}); err != nil {
		return "", &Error{Code: CodeUploadFailed, Op: "mkdir -p " + stagingRoot, Detail: err.Error()}
	}
	for _, a := range artifacts {
		stagingPath := path.Join(stagingRoot, a.Basename)
		if a.Directory {
			if err := comm.UploadDir(stagingPath, a.LocalPath, nil); err != nil {
				return "", &Error{Code: CodeUploadFailed, Op: "upload directory " + a.LocalPath, Detail: err.Error()}
			}
		} else {
			if err := uploadFile(comm, stagingPath, a.LocalPath); err != nil {
				return "", err
			}
		}
		if err := placeArtifact(ctx, comm, installDir, stagingPath, a, token); err != nil {
			return "", err
		}
	}
	return token, nil
}

// uploadFile streams the local file at localPath to the remote path dst via
// comm.Upload. On failure it returns a *Error with code UPLOAD_FAILED.
func uploadFile(comm packersdk.Communicator, dst, localPath string) error {
	f, err := os.Open(localPath)
	if err != nil {
		return &Error{Code: CodeUploadFailed, Op: "open " + localPath, Detail: err.Error()}
	}
	uploadErr := comm.Upload(dst, f, nil)
	closeErr := f.Close()
	if uploadErr != nil {
		return &Error{Code: CodeUploadFailed, Op: "upload " + localPath, Detail: uploadErr.Error()}
	}
	if closeErr != nil {
		return &Error{Code: CodeUploadFailed, Op: "close " + localPath, Detail: closeErr.Error()}
	}
	return nil
}

// placeArtifact atomically moves the uploaded artifact from its staging path
// into its final name inside installDir: stage under a sibling temp name,
// then plain mv into the final name, then normalize ownership to root:root.
func placeArtifact(ctx context.Context, comm packersdk.Communicator, installDir, stagingPath string, a Artifact, token string) error {
	tmpPath := path.Join(installDir, a.Basename+".tmp"+token)
	finalPath := path.Join(installDir, a.Basename)
	if _, _, err := run(ctx, comm, []string{"mv", stagingPath, tmpPath}); err != nil {
		return &Error{Code: CodeUploadFailed, Op: "mv " + stagingPath + " " + tmpPath, Detail: err.Error()}
	}
	if _, _, err := run(ctx, comm, []string{"mv", tmpPath, finalPath}); err != nil {
		return &Error{Code: CodeUploadFailed, Op: "mv " + tmpPath + " " + finalPath, Detail: err.Error()}
	}
	chownArgs := []string{"chown", "root:root", finalPath}
	if a.Directory {
		chownArgs = []string{"chown", "-R", "root:root", finalPath}
	}
	if _, _, err := run(ctx, comm, chownArgs); err != nil {
		return &Error{Code: CodeUploadFailed, Op: strings.Join(chownArgs, " "), Detail: err.Error()}
	}
	return nil
}

// randomToken returns a random alphanumeric token used for the private
// staging path and the atomic-placement temp-sibling suffix. The token
// always matches [A-Za-z0-9]+.
func randomToken() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err == nil {
		return hex.EncodeToString(b)
	}
	// crypto/rand failure is effectively unreachable; fall back to a
	// nanosecond timestamp so placement still produces a fresh token.
	return fmt.Sprintf("%x", time.Now().UnixNano())
}
