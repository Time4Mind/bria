package telegramcontroller

import (
	"context"

	"bria/internal/app"
	"bria/internal/domain"
)

func (c *Controller) releaseOnShutdown(ctx context.Context, process createdProcess) error {
	if attacher, ok := c.lifecycle.(app.SessionAttacher); ok && attacher.SupportsAttach(process.request.Provider) {
		return attacher.Detach(ctx, process.request, process.binding)
	}
	return c.lifecycle.Abort(ctx, process.request, process.binding)
}

func (c *Controller) canRecoverObservation(provider domain.Provider) bool {
	capability, ok := c.observer().(interface{ SupportsAttach(domain.Provider) bool })
	return c.recoverer != nil && ok && capability.SupportsAttach(provider)
}
