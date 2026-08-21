package sessioncontrol

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/clusterstate"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/interaction"
	"github.com/Time4Mind/bria/internal/processlog"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

type failRuntimePublishPort struct{ machine *clusterstate.Machine }

func (p failRuntimePublishPort) State() *domain.State { return p.machine.State() }

func (p failRuntimePublishPort) Apply(
	_ context.Context,
	command clusterstate.Command,
) (clusterstate.Result, error) {
	if command.Kind == clusterstate.CommandPublishSessionRuntime {
		return clusterstate.Result{}, errors.New("publish unavailable")
	}
	return p.machine.Apply(command), nil
}

func TestInteractionOperationLinkIsTransportNeutralAndContentFree(t *testing.T) {
	root := t.TempDir()
	manager, err := processlog.Start(root, processlog.Identity{Version: "test", Commit: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := interaction.NewIngress("test-ui", "secret-provider-event-42", "message")
	if err != nil {
		t.Fatal(err)
	}
	ctx := interaction.WithIngress(context.Background(), ingress)
	logInteractionOperation(
		ctx, domain.SessionRef{NodeID: "node", SessionID: "session"}, 3,
		"operation-7", string(runtimehost.ActionSendInput),
	)
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "detail-") {
			data, err = os.ReadFile(filepath.Join(root, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	row := string(data)
	for _, expected := range []string{
		"bria interaction: stage=operation_link", "adapter=test-ui", "kind=message",
		`operation="operation-7"`, `ref="node/session"`, "generation=3",
	} {
		if !strings.Contains(row, expected) {
			t.Fatalf("row missing %q: %q", expected, row)
		}
	}
	if strings.Contains(row, "secret-provider-event") || strings.Contains(row, "telegram") {
		t.Fatalf("transport payload leaked: %q", row)
	}
}

func TestAcceptedRuntimeOperationKeepsIngressLinkWhenStatePublishFails(t *testing.T) {
	controller, _, machine := controllerFixture(t)
	port := failRuntimePublishPort{machine: machine}
	service, err := application.NewService(port, port)
	if err != nil {
		t.Fatal(err)
	}
	controller.service = service
	root := t.TempDir()
	manager, err := processlog.Start(root, processlog.Identity{Version: "test", Commit: "abc"})
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := interaction.NewIngress("test-ui", "external-42", "message")
	if err != nil {
		t.Fatal(err)
	}
	ctx := interaction.WithIngress(context.Background(), ingress)
	if _, err := controller.SendInput(ctx, application.Principal{UserID: 7}, "operation-42", "sensitive-prompt"); err == nil {
		t.Fatal("runtime state publication unexpectedly succeeded")
	}
	if err := manager.Close(); err != nil {
		t.Fatal(err)
	}
	data := readInteractionDetailLog(t, root)
	if strings.Count(data, "stage=operation_link") != 1 ||
		!strings.Contains(data, `operation="operation-42"`) ||
		strings.Contains(data, "sensitive-prompt") {
		t.Fatalf("operation link=%q", data)
	}
}

func TestInteractionOperationLinkFlowCardinality(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *Controller, *clusterstate.Machine, context.Context) error
		want int
	}{
		{
			name: "normal",
			run: func(_ *testing.T, controller *Controller, _ *clusterstate.Machine, ctx context.Context) error {
				_, err := controller.SendInput(
					ctx, application.Principal{UserID: 7}, "normal-operation", "prompt",
				)
				return err
			},
			want: 1,
		},
		{
			name: "deferred",
			run: func(t *testing.T, controller *Controller, machine *clusterstate.Machine, ctx context.Context) error {
				setNodeStatus(t, machine, domain.NodeOffline, "offline-for-correlation")
				accepted, err := controller.SendInput(
					ctx, application.Principal{UserID: 7}, "deferred-operation", "prompt",
				)
				if err == nil && !accepted.Deferred {
					t.Fatal("input was not deferred")
				}
				return err
			},
			want: 1,
		},
		{
			name: "rejected",
			run: func(_ *testing.T, controller *Controller, _ *clusterstate.Machine, ctx context.Context) error {
				_, err := controller.SendInput(
					ctx, application.Principal{UserID: 999}, "rejected-operation", "prompt",
				)
				return err
			},
			want: 0,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			controller, _, machine := controllerFixture(t)
			root := t.TempDir()
			manager, err := processlog.Start(
				root, processlog.Identity{Version: "test", Commit: "abc"},
			)
			if err != nil {
				t.Fatal(err)
			}
			ingress, err := interaction.NewIngress("test-ui", test.name, "message")
			if err != nil {
				t.Fatal(err)
			}
			ctx := interaction.WithIngress(context.Background(), ingress)
			runErr := test.run(t, controller, machine, ctx)
			if test.want > 0 && runErr != nil {
				t.Fatal(runErr)
			}
			if test.want == 0 && runErr == nil {
				t.Fatal("rejected operation unexpectedly succeeded")
			}
			if err := manager.Close(); err != nil {
				t.Fatal(err)
			}
			if count := strings.Count(readInteractionDetailLog(t, root), "stage=operation_link"); count != test.want {
				t.Fatalf("operation links=%d want=%d", count, test.want)
			}
		})
	}
}

func readInteractionDetailLog(t *testing.T, root string) string {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	var result strings.Builder
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "detail-") {
			continue
		}
		data, readErr := os.ReadFile(filepath.Join(root, entry.Name()))
		if readErr != nil {
			t.Fatal(readErr)
		}
		result.Write(data)
	}
	return result.String()
}
