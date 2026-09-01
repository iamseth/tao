// Package prompts owns Tao's embedded prompt templates and the small registry
// used to render or install user-facing agent commands. PromptNames and
// Definitions expose only that installable registry.
//
// Merge resolution, aggregate merge review, and pull-request rework triage use
// separate templates and renderers in this package. Those prompts are internal
// workflow inputs and are intentionally excluded from the installable registry.
package prompts
