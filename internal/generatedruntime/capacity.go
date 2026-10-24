package generatedruntime

import (
	"context"
	"errors"

	"github.com/hostd/hostd/internal/generatedcapacity"
	"github.com/hostd/hostd/internal/generatedingress"
)

// capacityDiagnostic keeps the runtime's closed diagnostic mapping for the
// single domain-owned budget. Raw budget construction and leases remain
// private to generatedingress.
func capacityDiagnostic(ctx context.Context, err error) error {
	switch {
	case errors.Is(err, generatedcapacity.ErrInvalidLimits):
		return &Error{Code: DiagnosticInternalError}
	case errors.Is(err, generatedcapacity.ErrInvalidCount):
		return &Error{Code: DiagnosticValidationFailed}
	case errors.Is(err, generatedcapacity.ErrInsufficientCapacity):
		return &Error{Code: DiagnosticInsufficientReplacementSpace}
	case generatedingress.IsCode(err, generatedingress.DiagnosticCancelled):
		return &Error{Code: DiagnosticCancelled}
	case ctx != nil && errors.Is(ctx.Err(), context.Canceled), errors.Is(err, context.Canceled):
		return &Error{Code: DiagnosticCancelled}
	default:
		return &Error{Code: DiagnosticRuntimeUnavailable}
	}
}
