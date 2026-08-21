package telegramapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
)

func TestResponseCardCoordinatorSerializesOneUserAndCleansLane(t *testing.T) {
	coordinator := newResponseCardCoordinator()
	releaseFirst, err := coordinator.acquire(context.Background(), domain.UserID(7))
	if err != nil {
		t.Fatal(err)
	}
	acquiredSecond := make(chan func(), 1)
	go func() {
		release, acquireErr := coordinator.acquire(context.Background(), domain.UserID(7))
		if acquireErr == nil {
			acquiredSecond <- release
		}
	}()
	select {
	case release := <-acquiredSecond:
		release()
		t.Fatal("same-user lane was not serialized")
	case <-time.After(20 * time.Millisecond):
	}
	releaseFirst()
	select {
	case release := <-acquiredSecond:
		release()
	case <-time.After(time.Second):
		t.Fatal("same-user waiter did not acquire released lane")
	}
	if got := coordinator.size(); got != 0 {
		t.Fatalf("idle lanes=%d, want zero", got)
	}
}

func TestResponseCardCoordinatorAllowsDifferentUsers(t *testing.T) {
	coordinator := newResponseCardCoordinator()
	releaseFirst, err := coordinator.acquire(context.Background(), domain.UserID(7))
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond, err := coordinator.acquire(context.Background(), domain.UserID(8))
	if err != nil {
		t.Fatal(err)
	}
	releaseSecond()
	releaseFirst()
	if got := coordinator.size(); got != 0 {
		t.Fatalf("idle lanes=%d, want zero", got)
	}
}

func TestResponseCardCoordinatorCancelsWaiterAndCleansLane(t *testing.T) {
	coordinator := newResponseCardCoordinator()
	release, err := coordinator.acquire(context.Background(), domain.UserID(7))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = coordinator.acquire(ctx, domain.UserID(7)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled acquire error=%v", err)
	}
	release()
	if got := coordinator.size(); got != 0 {
		t.Fatalf("idle lanes=%d, want zero", got)
	}
}
