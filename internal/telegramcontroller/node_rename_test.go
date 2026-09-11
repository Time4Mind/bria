package telegramcontroller_test

import (
	"context"
	"strings"
	"testing"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/telegramcontroller"
	"bria/internal/telegramsettingsview"
)

type nodeRenamePreferences struct {
	*testProviderPreferences
	name        string
	renameCalls int
}

func (preferences *nodeRenamePreferences) NodeName(context.Context, domain.ComputerID) (string, error) {
	return preferences.name, nil
}

func (preferences *nodeRenamePreferences) RenameNode(_ context.Context, _ domain.ComputerID, name string) error {
	preferences.name = name
	preferences.renameCalls++
	return nil
}

func TestNodeRenamePublicUIShowsCurrentNameAndFailsClosed(t *testing.T) {
	preferences := &nodeRenamePreferences{
		testProviderPreferences: &testProviderPreferences{values: map[domain.Provider]telegramcontroller.ProviderPreference{}},
		name:                    "Current Node",
	}
	environment := &creationEnvironmentStub{computers: []sessioncreation.Computer{
		{ID: "local", Name: "Stale inventory name"},
		{ID: "worker", Name: "Busy Node"},
	}}
	controller := newController(t, nil, &memorySessions{}, nil, nil, telegramcontroller.Options{
		Settings: &testPreferences{}, Providers: preferences, CreationEnvironment: environment,
	})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	creation, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSettingsCategory, Choice: int(telegramsettingsview.CategoryCreation),
	})
	if err != nil || creation.Surface == nil || !strings.Contains(creation.Surface.Text, "| Нода | Current Node |") {
		t.Fatalf("creation settings do not show the persisted current name: result=%#v err=%v", creation, err)
	}
	if !hasSemanticButton(creation.Surface.Rows, "Переименовать ноду", telegramcontroller.SemanticSettingsRenameNode) {
		t.Fatalf("creation settings do not expose node rename: %#v", creation.Surface.Rows)
	}

	editor, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSettingsRenameNode,
	})
	if err != nil || editor.Surface == nil || !strings.Contains(editor.Surface.Text, "Current Node") {
		t.Fatalf("rename editor does not show the current copyable name: result=%#v err=%v", editor, err)
	}

	rejected, err := controller.HandleSemanticMessage(context.Background(), coordinator.Update{
		ID: 1, Kind: coordinator.UpdateMessage, ActorID: ownerID, ConversationID: chatID,
		ConversationKind: "private", Text: " \t ",
	})
	if err != nil || rejected.Surface == nil || !strings.Contains(rejected.Surface.Text, "непустым") {
		t.Fatalf("empty rename was not rejected visibly: result=%#v err=%v", rejected, err)
	}
	if preferences.name != "Current Node" || preferences.renameCalls != 0 {
		t.Fatalf("empty rename mutated name=%q calls=%d", preferences.name, preferences.renameCalls)
	}

	rejected, err = controller.HandleSemanticMessage(context.Background(), coordinator.Update{
		ID: 2, Kind: coordinator.UpdateMessage, ActorID: ownerID, ConversationID: chatID,
		ConversationKind: "private", Text: "busy node",
	})
	if err != nil || rejected.Surface == nil || !strings.Contains(rejected.Surface.Text, "уже используется") {
		t.Fatalf("colliding rename was not rejected visibly: result=%#v err=%v", rejected, err)
	}
	if preferences.name != "Current Node" || preferences.renameCalls != 0 {
		t.Fatalf("colliding rename mutated name=%q calls=%d", preferences.name, preferences.renameCalls)
	}

	rejected, err = controller.HandleSemanticMessage(context.Background(), coordinator.Update{
		ID: 3, Kind: coordinator.UpdateMessage, ActorID: ownerID, ConversationID: chatID,
		ConversationKind: "private", Text: "line one\nline two",
	})
	if err != nil || rejected.Surface == nil || !strings.Contains(rejected.Surface.Text, "Не удалось переименовать") {
		t.Fatalf("invalid rename was not rejected visibly: result=%#v err=%v", rejected, err)
	}
	if preferences.name != "Current Node" || preferences.renameCalls != 0 {
		t.Fatalf("invalid rename mutated name=%q calls=%d", preferences.name, preferences.renameCalls)
	}

	renamed, err := controller.HandleSemanticMessage(context.Background(), coordinator.Update{
		ID: 4, Kind: coordinator.UpdateMessage, ActorID: ownerID, ConversationID: chatID,
		ConversationKind: "private", Text: "  New Node  ",
	})
	if err != nil || renamed.Surface == nil || !strings.Contains(renamed.Surface.Text, "New Node") {
		t.Fatalf("valid rename was not confirmed with the persisted name: result=%#v err=%v", renamed, err)
	}
	if preferences.name != "New Node" || preferences.renameCalls != 1 {
		t.Fatalf("valid rename name=%q calls=%d", preferences.name, preferences.renameCalls)
	}

	creation, err = controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{
		Kind: telegramcontroller.SemanticSettingsCategory, Choice: int(telegramsettingsview.CategoryCreation),
	})
	if err != nil || creation.Surface == nil || !strings.Contains(creation.Surface.Text, "| Нода | New Node |") {
		t.Fatalf("reopened settings do not show renamed node: result=%#v err=%v", creation, err)
	}
}
