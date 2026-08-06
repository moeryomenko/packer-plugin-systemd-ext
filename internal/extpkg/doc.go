// Package extpkg implements the shared extension-image helpers used by both
// provisioners:
//
//   - ValidateName enforces the extension-name contract.
//   - WriteReleaseFile generates or overrides the extension-release file from
//     the guest os-release, including the SYSEXT_LEVEL/CONFEXT_LEVEL fields
//     when present.
//   - PackageDirectory lays a directory source out as usr/ (sysext) or etc/
//     (confext) plus its extension-release.d entry.
//   - PackageImage packages a directory source into a squashfs or erofs .raw
//     disk image.
//   - DetectVeritySet resolves the .verity/.roothash/.roothash.p7s companions
//     of a prebuilt .raw source.
package extpkg
