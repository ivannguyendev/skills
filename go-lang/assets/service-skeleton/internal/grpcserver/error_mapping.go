package grpcserver

import (
	"context"
	"errors"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"example.com/skeleton/internal/apperr"
)

// domainCodes maps transport-neutral error kinds to gRPC codes.
var domainCodes = []struct {
	err  error
	code codes.Code
}{
	{apperr.ErrNotFound, codes.NotFound},
	{apperr.ErrAlreadyExists, codes.AlreadyExists},
	{apperr.ErrInvalidArgument, codes.InvalidArgument},
	{apperr.ErrFailedPrecondition, codes.FailedPrecondition},
	{apperr.ErrPermissionDenied, codes.PermissionDenied},
	{apperr.ErrUnauthenticated, codes.Unauthenticated},
	{apperr.ErrUnavailable, codes.Unavailable},
}

// ToStatus converts err into a gRPC status error.
//
//   - a status returned directly by the handler is a deliberate decision and
//     passes through unchanged;
//   - context errors keep their meaning (Canceled / DeadlineExceeded);
//   - domain errors map to their code with only the sentinel text, because
//     wrap messages often contain SQL, hostnames or other tenants' IDs;
//   - a *wrapped* status came from a downstream call. Forwarding its code
//     would leak the dependency's semantics (its InvalidArgument is our bug),
//     so only codes that mean the same thing to our caller survive;
//   - anything else becomes Internal with a generic message; log the
//     original error server-side before discarding it.
func ToStatus(err error) error {
	if err == nil {
		return nil
	}
	if s, ok := err.(interface{ GRPCStatus() *status.Status }); ok {
		return s.GRPCStatus().Err()
	}
	switch {
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "deadline exceeded")
	}
	for _, d := range domainCodes {
		if errors.Is(err, d.err) {
			return status.Error(d.code, d.err.Error())
		}
	}
	if st, ok := status.FromError(err); ok {
		switch st.Code() {
		case codes.DeadlineExceeded:
			return status.Error(codes.DeadlineExceeded, "deadline exceeded")
		case codes.Canceled:
			return status.Error(codes.Canceled, "request canceled")
		case codes.Unavailable:
			return status.Error(codes.Unavailable, "dependency unavailable")
		default: // any other downstream code is our bug from the caller's view
		}
	}
	return status.Error(codes.Internal, "internal error")
}
