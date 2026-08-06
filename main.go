// main is the entry point for the systemd-ext Packer plugin. It registers
// the "sysext" and "confext" provisioners with their literal names (the SDK
// default-name sentinel is intentionally not used) and hands control to the
// SDK plugin server.
package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/packer-plugin-sdk/plugin"

	"github.com/eryoma/packer-plugin-systemd-ext/provisioner/confext"
	"github.com/eryoma/packer-plugin-systemd-ext/provisioner/sysext"
	"github.com/eryoma/packer-plugin-systemd-ext/version"
)

func main() {
	pps := plugin.NewSet()
	pps.RegisterProvisioner("sysext", new(sysext.Provisioner))
	pps.RegisterProvisioner("confext", new(confext.Provisioner))
	pps.SetVersion(version.PluginVersion)

	if err := pps.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
