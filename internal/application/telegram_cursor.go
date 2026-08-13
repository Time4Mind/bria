package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/Time4Mind/bria/internal/clusterstate"
)

type ReplicatedTelegramCursor struct {
	service *Service
}

func NewReplicatedTelegramCursor(service *Service) (*ReplicatedTelegramCursor, error) {
	if service == nil {
		return nil, errors.New("application service is required")
	}
	return &ReplicatedTelegramCursor{service: service}, nil
}

func (c *ReplicatedTelegramCursor) Load(context.Context) (int64, error) {
	state := c.service.reader.State()
	if state == nil {
		return 0, errors.New("state reader returned nil")
	}
	return state.TelegramNextUpdateID, nil
}

func (c *ReplicatedTelegramCursor) Commit(ctx context.Context, nextUpdateID int64) error {
	scope := fmt.Sprintf("telegram-cursor-%d", nextUpdateID)
	return c.service.AdvanceTelegramCursor(WithOperationScope(ctx, scope), nextUpdateID)
}

func (s *Service) TelegramNextUpdateID() int64 {
	state := s.reader.State()
	if state == nil {
		return 0
	}
	return state.TelegramNextUpdateID
}

func (s *Service) AdvanceTelegramCursor(ctx context.Context, nextUpdateID int64) error {
	return s.apply(
		ctx,
		clusterstate.CommandAdvanceTelegramCursor,
		clusterstate.AdvanceTelegramCursor{NextUpdateID: nextUpdateID},
	)
}
