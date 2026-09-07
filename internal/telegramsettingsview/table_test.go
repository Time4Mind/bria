package telegramsettingsview

import (
	"context"
	"strings"
	"testing"
)

func TestSettingsSurfacesAreRichTablesWithoutChangingNavigation(t *testing.T) {
	root := Render()
	if !strings.Contains(root.Text, "| Раздел |") {
		t.Fatalf("settings root is not a table: %q", root.Text)
	}
	for category := CategoryCard; category <= CategoryProviders; category++ {
		surface, err := RenderCategory(context.Background(), settingsPreferencesStub{}, nil, 16, category)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(surface.Text, "\n\n\u00a0\n\n| Настройка | Значение |") {
			t.Errorf("category%d lacks rich table: %q", category, surface.Text)
		}
	}
}

func TestTableEscapesNodeSpecificValuesWithoutInjectingRows(t *testing.T) {
	surface := AppendFields(table("Настройки", "Настройка", "Значение"), Field{Name: "Папка", Value: "/a|b\n<script>_x*`[test]"})
	if !surface.RichMarkdown || !strings.Contains(surface.Text, "| Папка | /a\\|b &lt;script&gt;\\_x\\*\\`\\[test\\] |") {
		t.Fatalf("unsafe table cell: %q", surface.Text)
	}
	if strings.Count(surface.Text, "\n| ") != 2 {
		t.Fatalf("value introduced table rows: %q", surface.Text)
	}
}
