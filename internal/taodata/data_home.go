package taodata

import (
	"os"
	"path/filepath"
)

// DataHome resolves Tao's centralized, inspectable runtime data directory.
func DataHome() string {
	return ResolveDataHome(os.Getenv)
}

// ResolveDataHome resolves the data home from TAO_DATA_HOME, XDG_DATA_HOME, or the user home.
func ResolveDataHome(getenv func(string) string) string {
	if dir := getenv("TAO_DATA_HOME"); dir != "" {
		return filepath.Clean(dir)
	}
	if dir := getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(filepath.Clean(dir), "tao")
	}
	if home := getenv("HOME"); home != "" {
		return filepath.Join(filepath.Clean(home), ".local", "share", "tao")
	}
	return filepath.Join(".", ".local", "share", "tao")
}
