package application

import (
	"context"
	"testing"

	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
)

type capturePort struct {
	commands []clusterstate.Command
	state    *domain.State
}

func (p *capturePort) State() *domain.State { return p.state.Clone() }
func (p *capturePort) Apply(_ context.Context, command clusterstate.Command) (clusterstate.Result, error) {
	p.commands = append(p.commands, command)
	return clusterstate.Result{OperationID: command.OperationID}, nil
}

func TestOperationScopeMakesReplayCommandIDStable(t *testing.T) {
	state := domain.NewState()
	port := &capturePort{state: state}
	service, err := NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithOperationScope(context.Background(), "telegram-update-42")
	for range 2 {
		if err := service.AdvanceTelegramCursor(ctx, 43); err != nil {
			t.Fatal(err)
		}
	}
	if len(port.commands) != 2 || port.commands[0].OperationID != port.commands[1].OperationID {
		t.Fatalf("scoped command IDs=%#v", port.commands)
	}
}
