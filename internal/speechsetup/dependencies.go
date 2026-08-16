package speechsetup

import (
	"context"

	"github.com/Time4Mind/bria/internal/systemdeps"
)

func (m *Manager) installSystemDependencies(ctx context.Context) error {
	return systemdeps.Ensure(ctx, m.config.Dependencies, systemdeps.ProfileSpeech, func() bool {
		_, err := executable(m.config.FFmpegCommand)
		return err == nil
	})
}
