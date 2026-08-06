// Package version exposes the plugin version. The source of truth is
// version/VERSION (e.g. "v0.1.0-dev"); Version/VersionPrerelease mirror it so
// the SDK PluginVersion can be constructed at package init.
package version

import "github.com/hashicorp/packer-plugin-sdk/version"

var (
	Version           = "v0.1.0"
	VersionPrerelease = "dev"
	VersionMetadata   = ""
	PluginVersion     = version.NewPluginVersion(Version, VersionPrerelease, VersionMetadata)
)
