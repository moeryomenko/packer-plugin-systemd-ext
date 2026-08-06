package extpkg

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// verityCompanions lists, in upload order, the companion suffixes that may
// accompany a prebuilt <base>.raw source: the dm-verity hash tree,
// the root hash, and the optional detached signature of the root hash.
var verityCompanions = []string{".verity", ".roothash", ".roothash.p7s"}

// DetectVeritySet inspects the directory of basePath (a prebuilt .raw source,
// e.g. /path/foo.raw) for its verity companions and returns the paths of every
// companion that must be uploaded alongside it. The
// companions of base name <base> are <base>.verity, <base>.roothash and
// <base>.roothash.p7s, in that deterministic order. A bare .raw with no
// companions is valid and returns an empty slice. If any companion exists but
// the set is incomplete, the returned error contains the code
// "INCOMPLETE_VERITY_SET" and names the missing companion file. Completeness
// rules: .verity and .roothash form a pair (one without the other is
// incomplete) and .roothash.p7s requires .roothash. Companion files whose
// base name differs from the .raw's are ignored.
func DetectVeritySet(basePath string) ([]string, error) {
	dir := filepath.Dir(basePath)
	base := strings.TrimSuffix(filepath.Base(basePath), filepath.Ext(basePath))

	present := make(map[string]bool, len(verityCompanions))
	for _, suffix := range verityCompanions {
		if _, err := os.Stat(filepath.Join(dir, base+suffix)); err == nil {
			present[suffix] = true
		}
	}
	if len(present) == 0 {
		return nil, nil
	}

	missing := func(want string) error {
		return fmt.Errorf("INCOMPLETE_VERITY_SET: verity set for %s is missing %s", filepath.Base(basePath), base+want)
	}
	switch {
	case present[".verity"] && !present[".roothash"]:
		return nil, missing(".roothash")
	case present[".roothash"] && !present[".verity"]:
		return nil, missing(".verity")
	case present[".roothash.p7s"] && !present[".roothash"]:
		return nil, missing(".roothash")
	}

	out := make([]string, 0, len(present))
	for _, suffix := range verityCompanions {
		if present[suffix] {
			out = append(out, filepath.Join(dir, base+suffix))
		}
	}
	return out, nil
}
