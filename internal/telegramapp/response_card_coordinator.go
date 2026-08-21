package telegramapp

import (
	"context"
	"sync"

	"github.com/Time4Mind/bria/internal/domain"
)

type responseCardLane struct {
	token chan struct{}
	refs  int
}

type responseCardCoordinator struct {
	mu    sync.Mutex
	lanes map[domain.UserID]*responseCardLane
}

func newResponseCardCoordinator() responseCardCoordinator {
	return responseCardCoordinator{lanes: make(map[domain.UserID]*responseCardLane)}
}

func (c *responseCardCoordinator) acquire(
	ctx context.Context,
	userID domain.UserID,
) (func(), error) {
	c.mu.Lock()
	if c.lanes == nil {
		c.lanes = make(map[domain.UserID]*responseCardLane)
	}
	lane := c.lanes[userID]
	if lane == nil {
		lane = &responseCardLane{token: make(chan struct{}, 1)}
		lane.token <- struct{}{}
		c.lanes[userID] = lane
	}
	lane.refs++
	c.mu.Unlock()

	select {
	case <-ctx.Done():
		c.releaseRef(userID, lane)
		return nil, ctx.Err()
	case <-lane.token:
		return func() {
			lane.token <- struct{}{}
			c.releaseRef(userID, lane)
		}, nil
	}
}

func (c *responseCardCoordinator) releaseRef(
	userID domain.UserID,
	lane *responseCardLane,
) {
	c.mu.Lock()
	defer c.mu.Unlock()
	lane.refs--
	if lane.refs == 0 && c.lanes[userID] == lane {
		delete(c.lanes, userID)
	}
}

func (c *responseCardCoordinator) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.lanes)
}
