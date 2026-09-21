// Package confext implements the "confext" provisioner, which applies
// systemd-confext extension images to the image under construction.
//
// The provisioner accepts a list of extension entries, each pointing at a
// local directory (packaged by the plugin) or a prebuilt .raw disk image. It
// validates the configuration at Prepare time and applies the extensions
// to the guest in Provision in bake or persist mode.
package confext

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/hashicorp/packer-plugin-sdk/common"
	packersdk "github.com/hashicorp/packer-plugin-sdk/packer"
	"github.com/hashicorp/packer-plugin-sdk/template/config"

	"github.com/eryoma/packer-plugin-systemd-ext/internal/extpkg"
	guestops "github.com/eryoma/packer-plugin-systemd-ext/internal/guestops"
)

const (
	// defaultCommand is the guest-side binary invoked for merge/unmerge
	// operations.
	defaultCommand = "systemd-confext"
	// defaultInstallDir is where extension images land on the guest.
	defaultInstallDir = "/var/lib/confexts"
)

// Extension describes one extension entry.
type Extension struct {
	// Name is the extension name; it becomes the image/directory name on the
	// guest and the release-file name.
	Name string `mapstructure:"name"`
	// Source is a local path: either a directory (packaged by the plugin) or
	// a prebuilt .raw disk image uploaded as-is.
	Source string `mapstructure:"source"`
	// Format is the packaging format for directory sources: "directory"
	// (default), "squashfs", or "erofs". It is ignored for .raw sources.
	Format string `mapstructure:"format"`
	// ReleaseFile is a user-supplied extension-release file that overrides
	// auto-generation. Directory sources only.
	ReleaseFile string `mapstructure:"release_file"`
}

// Config is the confext provisioner configuration.
type Config struct {
	common.PackerConfig `            mapstructure:",squash"`
	// Extensions lists the extension entries to apply.
	Extensions []Extension `mapstructure:"extensions"`
	// Mode is the persistence strategy: "bake" (default) or "persist".
	Mode string `mapstructure:"mode"`
	// MergeDuringBuild runs the merge during the build. Defaults to true when
	// the key is omitted; false is a meaningful explicit value.
	MergeDuringBuild bool `mapstructure:"merge_during_build"`
	// EnableOnBoot enables the standard systemd extension service. Defaults to
	// true for compatibility; callers that manage delayed activation set false.
	EnableOnBoot bool `mapstructure:"enable_on_boot"`
	// Command is the guest-side binary to invoke. Defaults to
	// systemd-confext.
	Command string `mapstructure:"command"`
	// GuestInstallDir is where extension images land on the guest. Defaults
	// to /var/lib/confexts.
	GuestInstallDir string `mapstructure:"guest_install_dir"`
}

//go:generate go run github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc mapstructure-to-hcl2 -type Config,Extension
//go:generate go run github.com/hashicorp/packer-plugin-sdk/cmd/packer-sdc struct-markdown

// Provisioner applies systemd-confext extensions to the guest.
type Provisioner struct {
	config Config
}

var (
	// nameRe is the allowed character class for extension names.
	nameRe = regexp.MustCompile(`^[A-Za-z0-9._-]+$`)
	// commandRe is the allowed character class for the guest command path; the
	// command is invoked without a shell.
	commandRe = regexp.MustCompile(`^[A-Za-z0-9/._-]+$`)
)

// rawHasKey reports whether any raw config map contains key. It detects an
// explicitly provided merge_during_build so its zero value is not conflated
// with an absent key (it defaults to true only when omitted).
func rawHasKey(raws []interface{}, key string) bool {
	for _, raw := range raws {
		if m, ok := raw.(map[string]interface{}); ok {
			if _, present := m[key]; present {
				return true
			}
		}
	}
	return false
}

// validateName checks the extension-name rules.
func validateName(name string) error {
	switch {
	case name == "":
		return errors.New("must not be empty")
	case len(name) > 255:
		return errors.New("must be at most 255 bytes")
	case !nameRe.MatchString(name):
		return errors.New("must match ^[A-Za-z0-9._-]+$")
	case name == "..":
		// The character class admits "..", but it is a path-traversal hazard
		// on the guest and is rejected by the shared name contract.
		return errors.New(`must not be ".."`)
	}
	return nil
}

// ConfigSpec returns the HCL2 configuration spec for the provisioner. The
// spec is generated from Config by packer-sdc (provisioner.hcl2spec.go).
func (p *Provisioner) ConfigSpec() hcldec.ObjectSpec {
	return p.config.FlatMapstructure().HCL2Spec()
}

// Prepare decodes the raw configuration and validates it, aggregating
// every violation into a single packersdk.MultiError. Every error names
// the offending field and, where applicable, the extension.
func (p *Provisioner) Prepare(raws ...interface{}) error {
	p.config = Config{}
	if err := config.Decode(&p.config, &config.DecodeOpts{PluginType: "confext"}, raws...); err != nil {
		return err
	}

	// Defaults.
	if p.config.Mode == "" {
		p.config.Mode = "bake"
	}
	if !rawHasKey(raws, "merge_during_build") {
		p.config.MergeDuringBuild = true
	}
	if !rawHasKey(raws, "enable_on_boot") {
		p.config.EnableOnBoot = true
	}
	if p.config.Command == "" {
		p.config.Command = defaultCommand
	}
	if p.config.GuestInstallDir == "" {
		p.config.GuestInstallDir = defaultInstallDir
	}

	var errs *packersdk.MultiError

	if len(p.config.Extensions) == 0 {
		errs = packersdk.MultiErrorAppend(errs, errors.New("extensions: must not be empty"))
	}

	if p.config.Mode != "bake" && p.config.Mode != "persist" {
		errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("mode: %q must be \"bake\" or \"persist\"", p.config.Mode))
	}

	for i := range p.config.Extensions {
		ext := &p.config.Extensions[i]
		label := fmt.Sprintf("extensions[%d] (name %q)", i, ext.Name)

		if err := validateName(ext.Name); err != nil {
			errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("%s: name: %v", label, err))
		}

		if ext.Source == "" {
			errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("%s: source: must not be empty", label))
			continue
		}
		info, statErr := os.Stat(ext.Source)
		if statErr != nil {
			errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("%s: source %q: %v", label, ext.Source, statErr))
			continue
		}
		if !info.IsDir() && !strings.HasSuffix(ext.Source, ".raw") {
			errs = packersdk.MultiErrorAppend(
				errs,
				fmt.Errorf("%s: source %q: neither a directory nor a .raw file", label, ext.Source),
			)
			continue
		}

		// Format applies to directory sources only.
		if info.IsDir() {
			format := ext.Format
			if format == "" {
				format = "directory"
			}
			switch format {
			case "directory":
				// No packaging tool needed.
			case "squashfs":
				if _, err := exec.LookPath("mksquashfs"); err != nil {
					errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("%s: format %q: mksquashfs not found on PATH", label, format))
				}
			case "erofs":
				if _, err := exec.LookPath("mkfs.erofs"); err != nil {
					errs = packersdk.MultiErrorAppend(errs, fmt.Errorf("%s: format %q: mkfs.erofs not found on PATH", label, format))
				}
			default:
				errs = packersdk.MultiErrorAppend(
					errs,
					fmt.Errorf("%s: format %q: must be directory, squashfs, or erofs", label, format),
				)
			}
		}

		if ext.ReleaseFile != "" {
			relInfo, relErr := os.Stat(ext.ReleaseFile)
			if relErr != nil || !relInfo.Mode().IsRegular() {
				errs = packersdk.MultiErrorAppend(
					errs,
					fmt.Errorf("%s: release_file %q: not a readable file", label, ext.ReleaseFile),
				)
				continue
			}
			f, openErr := os.Open(ext.ReleaseFile)
			if openErr != nil {
				errs = packersdk.MultiErrorAppend(
					errs,
					fmt.Errorf("%s: release_file %q: not readable: %v", label, ext.ReleaseFile, openErr),
				)
				continue
			}
			_ = f.Close()
		}
	}

	if !filepath.IsAbs(p.config.GuestInstallDir) {
		errs = packersdk.MultiErrorAppend(
			errs,
			fmt.Errorf("guest_install_dir: %q must be an absolute path", p.config.GuestInstallDir),
		)
	}

	if !commandRe.MatchString(p.config.Command) {
		errs = packersdk.MultiErrorAppend(
			errs,
			fmt.Errorf("command: %q contains characters outside [A-Za-z0-9/._-]", p.config.Command),
		)
	}

	if errs != nil {
		return errs
	}
	return nil
}

// Provision applies the configured extensions to the guest. Mode dispatch:
// bake and persist share the guest preflight, the one os-release read, and
// per-extension packaging + upload with atomic placement; only the
// post-placement sequence differs.
func (p *Provisioner) Provision(
	ctx context.Context,
	ui packersdk.Ui,
	comm packersdk.Communicator,
	generatedData map[string]interface{},
) error {
	if p.config.Mode == "persist" {
		return p.provisionPersist(ctx, ui, comm)
	}
	return p.provisionBake(ctx, ui, comm)
}

// provisionBake runs the bake flow for every configured extension: shared
// preflight + placement via provisionFlow, then per extension the merge
// validation with copy-into-rootfs and cleanup.
func (p *Provisioner) provisionBake(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator) error {
	const typ = guestops.TypeConfext
	return p.provisionFlow(
		ctx,
		ui,
		comm,
		typ,
		func(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, placed guestops.BakeExtension) error {
			if err := guestops.Bake(ctx, ui, comm, typ, p.config.Command, p.config.GuestInstallDir, placed); err != nil {
				return err
			}
			ui.Say(fmt.Sprintf("confext: baked extension %s", placed.Name))
			return nil
		},
	)
}

// provisionPersist runs the persist flow for every configured extension:
// shared preflight + placement via provisionFlow, then per extension the
// persist sequence (merge-state handling per merge_during_build,
// boot-service enablement and verification).
func (p *Provisioner) provisionPersist(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator) error {
	const typ = guestops.TypeConfext
	return p.provisionFlow(
		ctx,
		ui,
		comm,
		typ,
		func(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, placed guestops.BakeExtension) error {
			if err := guestops.Persist(ctx, ui, comm, typ, p.config.Command, guestops.PersistExtension{Name: placed.Name}, p.config.MergeDuringBuild, p.config.EnableOnBoot); err != nil {
				return err
			}
			ui.Say(fmt.Sprintf("confext: persisted extension %s", placed.Name))
			return nil
		},
	)
}

// provisionFlow runs the steps shared by bake and persist modes: the guest
// preflight, the one /etc/os-release read when a directory source needs
// release-file generation, and per extension the local packaging plus upload
// with atomic placement. For each placed extension it calls apply with the
// mode-specific post-placement sequence (bake or persist).
func (p *Provisioner) provisionFlow(
	ctx context.Context,
	ui packersdk.Ui,
	comm packersdk.Communicator,
	typ guestops.ExtensionType,
	apply func(ctx context.Context, ui packersdk.Ui, comm packersdk.Communicator, placed guestops.BakeExtension) error,
) error {
	if _, err := guestops.Preflight(ctx, ui, comm, typ, p.config.Command); err != nil {
		return err
	}

	// Read the guest /etc/os-release once per run, only when a directory
	// source needs release-file generation.
	var osRelease []byte
	if needsOSRelease(p.config.Extensions) {
		var sb strings.Builder
		if err := comm.Download("/etc/os-release", &sb); err != nil {
			return &guestops.Error{Code: guestops.CodeDownloadFailed, Op: "download /etc/os-release", Detail: err.Error()}
		}
		osRelease = []byte(sb.String())
	}

	for i := range p.config.Extensions {
		ext := &p.config.Extensions[i]
		info, err := os.Stat(ext.Source)
		if err != nil {
			return fmt.Errorf("%s provisioner: extension %q: stat source %q: %w", typ, ext.Name, ext.Source, err)
		}

		var placed guestops.BakeExtension
		if info.IsDir() {
			placed, err = p.packageAndPlaceDirectory(ctx, comm, typ, ext, bytes.NewReader(osRelease))
		} else {
			placed, err = p.placeRawSource(ctx, comm, typ, ext)
		}
		if err != nil {
			return err
		}

		if err := apply(ctx, ui, comm, placed); err != nil {
			return err
		}
	}
	return nil
}

// packageAndPlaceDirectory packages a directory source into a local staging
// tree and uploads it, atomically placing it into the guest install
// directory. For squashfs/erofs formats the staged tree is packaged
// into a <name>.raw disk image via extpkg.PackageImage, which is then treated
// exactly like a prebuilt .raw source: uploaded with its verity companions
// and baked via systemd-dissect.
func (p *Provisioner) packageAndPlaceDirectory(
	ctx context.Context,
	comm packersdk.Communicator,
	typ guestops.ExtensionType,
	ext *Extension,
	osRelease io.Reader,
) (guestops.BakeExtension, error) {
	format := ext.Format
	if format == "" {
		format = "directory"
	}

	stagingDir, err := os.MkdirTemp("", "packer-confext-")
	if err != nil {
		return guestops.BakeExtension{}, fmt.Errorf(
			"confext provisioner: extension %q: create staging dir: %w",
			ext.Name,
			err,
		)
	}
	defer func() { _ = os.RemoveAll(stagingDir) }()

	var override []byte
	if ext.ReleaseFile != "" {
		override, err = os.ReadFile(ext.ReleaseFile)
		if err != nil {
			return guestops.BakeExtension{}, fmt.Errorf(
				"confext provisioner: extension %q: read release_file %q: %w",
				ext.Name,
				ext.ReleaseFile,
				err,
			)
		}
	}

	// A squashfs/erofs directory source is packaged locally into <name>.raw
	// and then handled exactly like a prebuilt .raw source (upload + verity
	// companions, dissect bake).
	if format != "directory" {
		rawPath := filepath.Join(stagingDir, ext.Name+".raw")
		if err := extpkg.PackageImage(ext.Source, rawPath, ext.Name, extpkg.ImageFormat(format), osRelease, override, extpkg.TypeConfext); err != nil {
			return guestops.BakeExtension{}, fmt.Errorf("confext provisioner: extension %q: package: %w", ext.Name, err)
		}
		return p.placeRawImage(ctx, comm, typ, ext.Name, rawPath)
	}

	if err := extpkg.PackageDirectory(ext.Source, stagingDir, ext.Name, osRelease, extpkg.TypeConfext, override); err != nil {
		return guestops.BakeExtension{}, fmt.Errorf("confext provisioner: extension %q: package: %w", ext.Name, err)
	}

	artifacts := []guestops.Artifact{{Basename: ext.Name, LocalPath: stagingDir, Directory: true}}
	if _, err := guestops.UploadAndPlace(ctx, comm, typ, p.config.GuestInstallDir, artifacts); err != nil {
		return guestops.BakeExtension{}, err
	}
	return guestops.BakeExtension{Name: ext.Name, ImageFormat: "directory", Basenames: []string{ext.Name}}, nil
}

// placeRawSource uploads a prebuilt .raw source and its verity companions
// and atomically places the full set into the guest install directory.
func (p *Provisioner) placeRawSource(
	ctx context.Context,
	comm packersdk.Communicator,
	typ guestops.ExtensionType,
	ext *Extension,
) (guestops.BakeExtension, error) {
	return p.placeRawImage(ctx, comm, typ, ext.Name, ext.Source)
}

// placeRawImage uploads the .raw disk image at sourcePath — a prebuilt .raw
// source or a directory source packaged to squashfs/erofs — and its verity
// companions and atomically places the full set into the guest install
// directory.
func (p *Provisioner) placeRawImage(
	ctx context.Context,
	comm packersdk.Communicator,
	typ guestops.ExtensionType,
	name, sourcePath string,
) (guestops.BakeExtension, error) {
	companions, err := extpkg.DetectVeritySet(sourcePath)
	if err != nil {
		return guestops.BakeExtension{}, fmt.Errorf("confext provisioner: extension %q: %w", name, err)
	}
	artifacts := make([]guestops.Artifact, 0, 1+len(companions))
	basenames := make([]string, 0, 1+len(companions))
	base := filepath.Base(sourcePath)
	artifacts = append(artifacts, guestops.Artifact{Basename: base, LocalPath: sourcePath})
	basenames = append(basenames, base)
	for _, c := range companions {
		cb := filepath.Base(c)
		artifacts = append(artifacts, guestops.Artifact{Basename: cb, LocalPath: c})
		basenames = append(basenames, cb)
	}
	if _, err := guestops.UploadAndPlace(ctx, comm, typ, p.config.GuestInstallDir, artifacts); err != nil {
		return guestops.BakeExtension{}, err
	}
	return guestops.BakeExtension{Name: name, ImageFormat: "raw", Basenames: basenames}, nil
}

// needsOSRelease reports whether any directory source requires release-file
// generation from the guest's /etc/os-release: a directory source without a
// user-supplied release_file override.
func needsOSRelease(exts []Extension) bool {
	for i := range exts {
		if exts[i].ReleaseFile != "" {
			continue
		}
		info, err := os.Stat(exts[i].Source)
		if err == nil && info.IsDir() {
			return true
		}
	}
	return false
}
