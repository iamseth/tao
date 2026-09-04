package cli

import (
	"context"

	"github.com/iamseth/tao/internal/clipboard"
)

type uiClipboard struct {
	session clipboard.Session
}

func newUIClipboard(app App) *uiClipboard {
	return &uiClipboard{session: clipboard.Session{Error: app.noteErrorOutput()}}
}

func (service *uiClipboard) Copy(ctx context.Context, text string) error {
	return service.session.Copy(ctx, text)
}
