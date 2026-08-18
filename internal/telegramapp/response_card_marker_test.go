package telegramapp

import (
	"testing"

	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestClusterUpdateCardMarkerIsDurableAndClearsOnNavigation(t *testing.T) {
	message := telegrambot.Message{PaneHash: "old-pane"}
	update := telegramui.Screen{Grid: telegramui.Grid{telegramui.Row{{
		Label: "refresh", Callback: telegramui.Callback{Action: telegramui.ActionClusterUpdateRefresh},
	}}}}
	if got := responseCardPaneHash(message, update); got != clusterUpdateCardMarker {
		t.Fatalf("update marker=%q", got)
	}
	menu := telegramui.Screen{Grid: telegramui.Grid{telegramui.Row{{
		Label: "menu", Callback: telegramui.Callback{Action: telegramui.ActionMenu},
	}}}}
	if got := responseCardPaneHash(telegrambot.Message{}, menu); got != "" {
		t.Fatalf("navigation retained update marker=%q", got)
	}
}
