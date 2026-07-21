// Package merge owns plan integration and the complete durable batch merge
// state machine. Callers construct its services and coordinator, invoke them,
// and render results without sequencing batch phases.
package merge
