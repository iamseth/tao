// Package promptinstall installs Tao's bundled slash-command prompts into each
// supported agent's configuration directory.
//
// It owns target-directory resolution per agent kind, force/skip semantics, and
// the special-cased Pi extension deployment. Prompt content is owned by the
// prompts package and agent identity by internal/runtimeconfig; this package only
// renders and writes those definitions to disk.
package promptinstall
