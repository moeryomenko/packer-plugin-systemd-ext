package extpkg

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// targetPrefix returns the subtree under dstDir where the source contents of
// an extension of type typ are staged: "usr" for sysexts, "etc" for
// confexts. An unsupported type returns an empty prefix and an error.
func targetPrefix(typ ExtensionType) (string, error) {
	switch typ {
	case TypeSysext:
		return "usr", nil
	case TypeConfext:
		return "etc", nil
	default:
		return "", fmt.Errorf("type: unsupported extension type %d", typ)
	}
}

// copyTree copies the directory tree rooted at srcDir into root, preserving
// directory and file modes (permissions plus setuid/setgid/sticky bits) and
// copying symlinks as symlinks (never dereferenced). The root directory itself
// is not recreated; only its contents are staged under root.
func copyTree(srcDir, root string) error {
	return filepath.WalkDir(srcDir, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(srcDir, path)
		if err != nil {
			return fmt.Errorf("relative path of %q: %w", path, err)
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			info, err := d.Info()
			if err != nil {
				return fmt.Errorf("stat source directory %q: %w", path, err)
			}
			if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
				return fmt.Errorf("create directory %q: %w", dst, err)
			}
			// MkdirAll's perm is masked by the umask; reapply the exact mode
			// so setuid/setgid/sticky bits survive staging.
			if err := os.Chmod(dst, info.Mode()); err != nil {
				return fmt.Errorf("chmod directory %q: %w", dst, err)
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			target, err := os.Readlink(path)
			if err != nil {
				return fmt.Errorf("readlink %q: %w", path, err)
			}
			if err := os.Symlink(target, dst); err != nil {
				return fmt.Errorf("create symlink %q: %w", dst, err)
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return fmt.Errorf("unsupported source entry %q (mode %v)", path, d.Type())
		}
		if err := copyFile(path, dst, d); err != nil {
			return err
		}
		return nil
	})
}

// copyFile copies the regular file at src to dst, preserving the source mode
// bits (permissions plus setuid/setgid/sticky).
func copyFile(src, dst string, d os.DirEntry) error {
	info, err := d.Info()
	if err != nil {
		return fmt.Errorf("stat source file %q: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source %q: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, info.Mode().Perm())
	if err != nil {
		return fmt.Errorf("create destination %q: %w", dst, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return fmt.Errorf("copy %q to %q: %w", src, dst, err)
	}
	if err := out.Close(); err != nil {
		return fmt.Errorf("close destination %q: %w", dst, err)
	}
	// OpenFile's perm is masked by the umask; reapply the exact mode so
	// setuid/setgid/sticky bits survive staging.
	if err := os.Chmod(dst, info.Mode()); err != nil {
		return fmt.Errorf("chmod destination %q: %w", dst, err)
	}
	return nil
}

// PackageDirectory stages a directory source into an extension tree rooted at
// dstDir. For TypeSysext the source contents are copied under
// dstDir/usr/; for TypeConfext under dstDir/etc/. The extension-release file
// is written into that staged tree by WriteReleaseFile (generated from
// osRelease, or verbatim when override is non-nil). File and directory modes
// (permissions plus setuid/setgid/sticky bits) and symlinks are preserved
// through staging, so the resulting tree can be consumed by the squashfs/erofs
// packagers unchanged. name is validated with ValidateName before any staging:
// an invalid name fails and leaves dstDir untouched. srcDir must be a
// directory; an empty directory is valid and stages just the release file.
func PackageDirectory(srcDir, dstDir, name string, osRelease io.Reader, typ ExtensionType, override []byte) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	info, err := os.Stat(srcDir)
	if err != nil {
		return fmt.Errorf("source %q: %w", srcDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("source %q is not a directory", srcDir)
	}
	prefix, err := targetPrefix(typ)
	if err != nil {
		return err
	}
	if err := copyTree(srcDir, filepath.Join(dstDir, prefix)); err != nil {
		return fmt.Errorf("stage source %q: %w", srcDir, err)
	}
	if err := WriteReleaseFile(dstDir, name, osRelease, typ, override); err != nil {
		return fmt.Errorf("write release file: %w", err)
	}
	return nil
}
