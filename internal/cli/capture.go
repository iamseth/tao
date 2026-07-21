package cli

import (
	"context"
	"flag"
	"fmt"
)

const planningSessionCaptureRemovedMessage = "planning-session capture is no longer supported"

var capturePlanningSessionCommand = commandMetadata{
	name:                  "capture-planning-session",
	minPrefix:             "cap",
	usageLines:            []string{"capture-planning-session (cap) --plan-dir DIR  (removed; planning-session capture is no longer supported)"},
	completionDescription: "Report removed planning-session capture support",
	long:                  "Report that planning-session capture support has been removed and is no longer supported. This legacy command is kept only so old wrappers fail with an honest error instead of creating planning-session sidecars.",
	registerFlags:         registerCapturePlanningSessionFlags,
	execute: func(c commandContext) error {
		return c.app.capturePlanningSession(c.ctx, c.args)
	},
}

func registerCapturePlanningSessionFlags(fs *flag.FlagSet) {
	fs.String("plan-dir", "", "plan directory that would have received planning-session artifacts")
	fs.String("session-id", "", "ignored; planning-session capture has been removed")
	fs.String("planning-started-at", "", "ignored; planning-session capture has been removed")
	fs.Bool("raw", false, "ignored; planning-session capture has been removed")
}

func (a App) capturePlanningSession(_ context.Context, args []string) error {
	fs, positional, err := a.parseArgs("capture-planning-session", args, registerCapturePlanningSessionFlags)
	if err != nil {
		return err
	}
	if err := requireNoArgs(positional, "usage: tao capture-planning-session --plan-dir DIR"); err != nil {
		return err
	}
	if flagStringValue(fs, "plan-dir") == "" {
		return fmt.Errorf("--plan-dir is required")
	}
	return fmt.Errorf("%s", planningSessionCaptureRemovedMessage)
}
