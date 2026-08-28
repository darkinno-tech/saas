package tenant

import "github.com/darkinno-tech/saas"

var (
	// ErrInvalidState reports an invalid tenant lifecycle transition.
	ErrInvalidState = saas.ErrInvalidState

	// ErrHostRequired reports that an operation requires host-side context.
	ErrHostRequired = saas.ErrHostRequired
)
