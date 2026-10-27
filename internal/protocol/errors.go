package protocol

import (
	"errors"
	"fmt"
)

// Transport and request error codes. The first group comes from docs/protocol/v1.md; the
// protocol.* group covers malformed input that never reaches a request handler.
const (
	CodeInvalidArgument     = "invalid_argument"
	CodeUnauthorized        = "unauthorized"
	CodeForbidden           = "forbidden"
	CodeNotFound            = "not_found"
	CodeConflict            = "conflict"
	CodeUnavailable         = "unavailable"
	CodeIncompatibleVersion = "incompatible_version"
	CodeWorkspaceBusy       = "workspace_busy"
	CodeHarnessUnavailable  = "harness_unavailable"
	CodeProviderTimeout     = "provider_timeout"
	CodeProviderRefused     = "provider_refused"
	CodeLowConfidence       = "low_confidence"
	CodeNoDecider           = "no_decider" // no decision model is set
	CodeOwnerWork           = "owner_work" // a folder about to be deleted holds the owner's work

	CodeTruncatedFrame   = "protocol.truncated_frame"
	CodeFrameTooLarge    = "protocol.frame_too_large"
	CodeUnknownFrameType = "protocol.unknown_frame_type"
	CodeInvalidChannel   = "protocol.invalid_channel"
	CodeInvalidUTF8      = "protocol.invalid_utf8"
	CodeUnsupported      = "protocol.unsupported"
)

// Error is a protocol error carrying a stable, machine-readable code.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Errorf builds a coded protocol error.
func Errorf(code, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// CodeOf reports the stable code of err, or invalid_argument for an unclassified error.
func CodeOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	return CodeInvalidArgument
}
