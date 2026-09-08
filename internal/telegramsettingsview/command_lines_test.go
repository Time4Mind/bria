package telegramsettingsview

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"bria/internal/settingsport"
)

type commandLinesPreferences struct{ settingsPreferencesStub }

func (commandLinesPreferences) CycleTechnicalCommandLines(context.Context) error { return nil }
func (commandLinesPreferences) CycleTechnicalOutputLines(context.Context) error  { return nil }

func TestCommandAndOutputControlsHaveIndependentLabelsAndDefaultValues(t *testing.T) {
	surface, err := RenderCategory(context.Background(), commandLinesPreferences{}, nil, 16, CategoryCard)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ label, action string }{
		{"Строки команды", "settings_technical_command_lines"},
		{"Строки технического вывода", "settings_technical_output_lines"},
	} {
		if !strings.Contains(surface.Text, "| "+tc.label+" | 10 |") {
			t.Errorf("missing default field %q", tc.label)
		}
		found := 0
		for _, row := range surface.Rows {
			for _, b := range row {
				if b.Action == tc.action && b.Label == tc.label && b.Choice == 0 {
					found++
				}
			}
		}
		if found != 1 {
			t.Errorf("control %q count=%d", tc.action, found)
		}
		if category, ok := CategoryForAction(tc.action); !ok || category != CategoryCard {
			t.Errorf("category for %q=%v,%v", tc.action, category, ok)
		}
	}
}

type independentLinesPreferences struct {
	commandLinesPreferences
	command, output int
}

func (p independentLinesPreferences) Snapshot(context.Context) (settingsport.Snapshot, error) {
	return settingsport.Snapshot{TechnicalCommandLines: p.command, TechnicalOutputLines: p.output}, nil
}

func TestCommandAndOutputViewsPreserveEveryIndependentChoice(t *testing.T) {
	for _, command := range []int{3, 5, 10, 20} {
		for _, output := range []int{3, 5, 10, 20} {
			surface, err := RenderCategory(context.Background(), independentLinesPreferences{command: command, output: output}, nil, 16, CategoryCard)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{fmt.Sprintf("| Строки команды | %d |", command), fmt.Sprintf("| Строки технического вывода | %d |", output)} {
				if !strings.Contains(surface.Text, want) {
					t.Fatalf("missing independently selected field %q", want)
				}
			}
		}
	}
}

func TestCommandControlRequiresOptionalCapability(t *testing.T) {
	surface, err := RenderCategory(context.Background(), settingsPreferencesStub{}, nil, 16, CategoryCard)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range surface.Rows {
		for _, b := range row {
			if b.Action == "settings_technical_command_lines" {
				t.Fatal("unsupported mutation advertised")
			}
		}
	}
}
