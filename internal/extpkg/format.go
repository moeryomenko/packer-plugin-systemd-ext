package extpkg

// ImageFormat selects which disk-image format PackageImage produces
// (supported formats are squashfs and erofs).
type ImageFormat string

const (
	// FormatSquashfs packages the staged tree with mksquashfs.
	FormatSquashfs ImageFormat = "squashfs"
	// FormatErofs packages the staged tree with mkfs.erofs.
	FormatErofs ImageFormat = "erofs"
)
