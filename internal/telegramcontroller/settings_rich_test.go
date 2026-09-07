package telegramcontroller

import (
	"context"
	"reflect"
	"testing"

	"bria/internal/telegramsettingsview"
)

func TestSettingsProjectionPreservesRichTableAndCategoryButtons(t *testing.T) {
	controller := &Controller{}
	surface := telegramsettingsview.Render()
	result, err := controller.projectSettingsSurface(context.Background(), surface, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Surface == nil || !result.Surface.RichMarkdown || result.Surface.Text != surface.Text {
		t.Fatalf("rich settings projection lost: %#v", result.Surface)
	}
	for i, row := range surface.Rows {
		for j, button := range row {
			want := SemanticButton{Label: button.Label, Action: SemanticActionKind(button.Action), Choice: button.Choice}
			if !reflect.DeepEqual(result.Surface.Rows[i][j], want) {
				t.Fatalf("button %d,%d changed", i, j)
			}
		}
	}
}
