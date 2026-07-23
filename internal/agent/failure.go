package agent

import "errors"

// RetryableTransportFailure marks a provider error that identifies an
// explicitly structured, transient transport failure. Leaf provider packages
// implement the interface without importing this package.
type RetryableTransportFailure interface {
	error
	RetryableTransportFailure()
}

// IsRetryableTransportFailure reports whether err or an error in its unwrap
// chain is explicitly marked as a retryable transport failure.
func IsRetryableTransportFailure(err error) bool {
	var failure RetryableTransportFailure
	return errors.As(err, &failure)
}
