package guestops

import "fmt"

// Code is a stable machine-readable error code for guest runtime failures
// Codes are part of the public contract: callers compare against
// them with errors.As, and every error message embeds the code text.
type Code string

const (
	// CodeGuestCommandNotFound reports that the configured guest command
	// (e.g. systemd-sysext) does not resolve on the guest.
	CodeGuestCommandNotFound Code = "GUEST_COMMAND_NOT_FOUND"
	// CodeSystemdTooOld reports that the guest systemd major version is below
	// the floor for the extension type: 252 for sysext, 254 for confext. The
	// message includes the detected version.
	CodeSystemdTooOld Code = "SYSTEMD_TOO_OLD"
	// CodeNotRoot reports that the guest preflight found the plugin not
	// running with uid 0.
	CodeNotRoot Code = "NOT_ROOT"
)

// Error is a typed guest error. It carries a stable
// machine-readable Code and a message that embeds the code, the failing
// operation, and detail such as the exit code and a tail of guest stderr.
// Use errors.As to extract it from a wrapped error chain.
type Error struct {
	// Code is the stable machine-readable error code.
	Code Code
	// Op is the failing guest command or operation, e.g. "systemctl
	// --version" or "preflight command systemd-sysext".
	Op string
	// Detail is human-readable context: exit code and stderr tail, the
	// detected systemd version, or the offending root uid.
	Detail string
}

// Error implements the error interface. The message includes the operation,
// the detail, and the machine-readable code.
func (e *Error) Error() string {
	return fmt.Sprintf("%s: %s [%s]", e.Op, e.Detail, e.Code)
}
