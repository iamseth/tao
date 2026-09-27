// Package theme owns every ANSI color sequence Tao emits. It is a leaf package
// that imports only internal/term and the standard library; terminal detection,
// profile conversion, and semantic palettes stay independent of their consumers.
package theme
