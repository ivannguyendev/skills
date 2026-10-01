// Package apperr defines transport-neutral error kinds for business code.
//
// Domain packages return these, wrapped with %w for context
// (fmt.Errorf("load order %s: %w", id, apperr.ErrNotFound)), and never import
// gRPC. The transport layer (grpcserver.ToStatus) maps them to status codes.
package apperr

import "errors"

// Error kinds. The text of each sentinel is what callers may see, so keep it
// generic; put details in the wrapping message, which stays server-side.
var (
	ErrNotFound           = errors.New("not found")
	ErrAlreadyExists      = errors.New("already exists")
	ErrInvalidArgument    = errors.New("invalid argument")
	ErrFailedPrecondition = errors.New("failed precondition")
	ErrPermissionDenied   = errors.New("permission denied")
	ErrUnauthenticated    = errors.New("unauthenticated")
	// ErrUnavailable marks a dependency outage the caller may retry later.
	ErrUnavailable = errors.New("dependency unavailable")
)
