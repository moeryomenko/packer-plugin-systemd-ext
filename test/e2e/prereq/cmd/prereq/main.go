// Command prereq is the command entry point for the fail-fast host
// prerequisite aggregate used by the E2E harnesses.
//
// It calls prereq.RunAll, prints the first actionable error naming the
// missing requirement to stderr, and exits 1 on failure. On success it
// prints a confirmation and exits 0. The harness run.sh scripts invoke this
// command before any plugin build or VM boot so a host without e.g. /dev/kvm
// is stopped immediately with a clear message.
package main

import (
	"fmt"
	"os"

	"github.com/eryoma/packer-plugin-systemd-ext/test/e2e/prereq"
)

func main() {
	if err := prereq.RunAll(); err != nil {
		fmt.Fprintf(os.Stderr, "E2E prereq FAIL: %v\n", err)
		os.Exit(1)
	}
	fmt.Println("E2E prereq: all host prerequisites satisfied")
}
