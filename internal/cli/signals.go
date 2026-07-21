package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

type commandSignalContextFunc func(context.Context) (context.Context, context.CancelFunc)

var newCommandSignalContext commandSignalContextFunc = func(ctx context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
}
