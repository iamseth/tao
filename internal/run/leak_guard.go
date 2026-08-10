package run

import "github.com/iamseth/tao/internal/agentsession"

// ControlCheckoutLeakError remains available from run for compatibility while
// bounded-session leak detection is owned by internal/agentsession.
type ControlCheckoutLeakError = agentsession.ControlCheckoutLeakError
