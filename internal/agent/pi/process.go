package pi

import "github.com/iamseth/tao/internal/agent/process"

// Process and ProcessStarter alias the shared agent subprocess primitives so the
// pi client and its tests reference one implementation. See
// internal/agent/process.
type Process = process.Process

type ProcessStarter = process.ProcessStarter

// DefaultProcessStarter spawns pi subprocesses via the shared starter.
var DefaultProcessStarter ProcessStarter = process.DefaultProcessStarter
