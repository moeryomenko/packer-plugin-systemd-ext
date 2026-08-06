package extpkg

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtensionType selects which extension-release layout to generate.
type ExtensionType int

const (
	// TypeSysext writes usr/lib/extension-release.d/extension-release.<name>.
	TypeSysext ExtensionType = iota
	// TypeConfext writes etc/extension-release.d/extension-release.<name>.
	TypeConfext
)

// WriteReleaseFile creates the extension-release file for an extension named
// name inside the packaged tree rooted at dstDir, creating parent directories
// as needed. For TypeSysext the file is usr/lib/extension-release.d/
// extension-release.<name>; for TypeConfext it is etc/extension-release.d/
// extension-release.<name>.
//
// When override is nil, the file is generated from osRelease: the ID= and
// VERSION_ID= lines are required and written first, followed by SYSEXT_LEVEL=
// (TypeSysext) or CONFEXT_LEVEL= (TypeConfext) when the guest defines it. No
// other os-release fields are copied and the output always ends with a
// trailing newline. When override is non-nil it must be non-empty; its bytes
// are written verbatim at the same path, bypassing os-release parsing
// entirely.
func WriteReleaseFile(dstDir, name string, osRelease io.Reader, typ ExtensionType, override []byte) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	if override != nil && len(override) == 0 {
		return errors.New("override: empty override bytes")
	}

	var rel string
	switch typ {
	case TypeSysext:
		rel = "usr/lib/extension-release.d/extension-release." + name
	case TypeConfext:
		rel = "etc/extension-release.d/extension-release." + name
	default:
		return fmt.Errorf("type: unsupported extension type %d", typ)
	}
	dst := filepath.Join(dstDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("create release directory: %w", err)
	}
	if override != nil {
		return os.WriteFile(dst, override, 0o644)
	}

	vals, err := parseOSRelease(osRelease)
	if err != nil {
		return err
	}
	id, ok := vals["ID"]
	if !ok || id == "" {
		return errors.New("os-release: missing required field ID")
	}
	versionID, ok := vals["VERSION_ID"]
	if !ok || versionID == "" {
		return errors.New("os-release: missing required field VERSION_ID")
	}

	var sb strings.Builder
	sb.WriteString("ID=" + id + "\n")
	sb.WriteString("VERSION_ID=" + versionID + "\n")
	switch typ {
	case TypeSysext:
		if v, ok := vals["SYSEXT_LEVEL"]; ok {
			sb.WriteString("SYSEXT_LEVEL=" + v + "\n")
		}
	case TypeConfext:
		if v, ok := vals["CONFEXT_LEVEL"]; ok {
			sb.WriteString("CONFEXT_LEVEL=" + v + "\n")
		}
	}
	return os.WriteFile(dst, []byte(sb.String()), 0o644)
}

// parseOSRelease parses os-release(5) content into a key/value map. Each line
// is a KEY=VALUE assignment; values may be quoted with single or double quotes
// (stripped), comment lines starting with '#' and blank lines are ignored,
// CRLF line endings are tolerated, and lines without '=' are skipped. A read
// error from the underlying reader is propagated unchanged.
func parseOSRelease(r io.Reader) (map[string]string, error) {
	vals := make(map[string]string)
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue // malformed line without an assignment
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		if len(val) >= 2 {
			if (val[0] == '"' && val[len(val)-1] == '"') || (val[0] == '\'' && val[len(val)-1] == '\'') {
				val = val[1 : len(val)-1]
			}
		}
		vals[key] = val
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return vals, nil
}
