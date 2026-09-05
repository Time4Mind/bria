// Package telegramcreationview renders the ephemeral session-creation wizard.
package telegramcreationview

import (
	"fmt"
	"strings"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
)

type Button struct {
	Label  string
	Action string
	Choice int
}

type Surface struct {
	Text string
	Rows [][]Button
}

func Render(snapshot sessioncreation.Snapshot) Surface {
	lines := []string{"Новая сессия"}
	rows := make([][]Button, 0, 14)
	if snapshot.ValidationError != "" {
		lines = append(lines, "Ошибка: "+snapshot.ValidationError)
	}
	switch snapshot.Step {
	case sessioncreation.StepComputer:
		lines = append(lines, "Выберите компьютер.")
		for index, computer := range snapshot.Computers {
			rows = append(rows, []Button{{Label: computer.Name, Action: "create_choice", Choice: index + 1}})
		}
	case sessioncreation.StepProvider:
		lines = append(lines, "Выберите бэкенд.")
		for _, capability := range snapshot.Providers {
			if !capability.Installed {
				continue
			}
			label, action := providerName(capability.Provider), "create_select_codex"
			if !capability.Enabled {
				label += " · включить"
			}
			if capability.Provider == domain.ProviderClaude {
				action = "create_select_claude"
			}
			rows = append(rows, []Button{{Label: label, Action: action}})
		}
	case sessioncreation.StepInstallRequired:
		lines = append(lines, "На компьютере не установлен ни один бэкенд.")
		rows = append(rows, []Button{{Label: "Настройки", Action: "menu_settings"}})
	case sessioncreation.StepDirectory:
		if snapshot.CurrentDirectory == "" {
			lines = append(lines, "Выберите корень.")
		} else {
			lines = append(lines, "Папка: "+snapshot.CurrentDirectory)
		}
		for index := 0; index < len(snapshot.Directories); index += 2 {
			row := []Button{{Label: "📁 " + snapshot.Directories[index].Name, Action: "create_choice", Choice: index + 1}}
			if index+1 < len(snapshot.Directories) {
				row = append(row, Button{Label: "📁 " + snapshot.Directories[index+1].Name, Action: "create_choice", Choice: index + 2})
			}
			rows = append(rows, row)
		}
		if snapshot.Pages > 1 {
			rows = append(rows, []Button{{Label: "◀", Action: "create_previous"}, {Label: fmt.Sprintf("%d/%d", snapshot.Page, snapshot.Pages), Action: "create_first"}, {Label: "▶", Action: "create_next"}})
		}
		if snapshot.CurrentDirectory != "" {
			rows = append(rows, []Button{{Label: "..", Action: "create_up"}, {Label: "Выбрать", Action: "create_pick"}, {Label: "Меню", Action: "menu_back"}})
			rows = append(rows, []Button{{Label: "Создать папку", Action: "create_directory_new"}})
		}
	case sessioncreation.StepDirectoryName:
		lines = append(lines, "Отправьте отдельным сообщением имя дочерней папки.")
	case sessioncreation.StepRecommendation:
		lines = append(lines, "Продолжить архивную сессию?")
		for index, item := range snapshot.Recommendations {
			rows = append(rows, []Button{{Label: item.Label, Action: "create_choice", Choice: index + 1}})
		}
		if snapshot.Pages > 1 {
			rows = append(rows, []Button{{Label: "◀", Action: "create_previous"}, {Label: fmt.Sprintf("%d/%d", snapshot.Page, snapshot.Pages), Action: "create_first"}, {Label: "▶", Action: "create_next"}})
		}
		rows = append(rows, []Button{{Label: "➕ Новая", Action: "create_fresh"}})
	default:
		lines = append(lines, "Нет доступных компьютеров.")
	}
	if snapshot.Step != sessioncreation.StepComputer && snapshot.Step != sessioncreation.StepUnavailable {
		rows = append(rows, []Button{{Label: "Назад", Action: "create_back"}})
	}
	if snapshot.Step != sessioncreation.StepDirectory || snapshot.CurrentDirectory == "" {
		rows = append(rows, []Button{{Label: "≡ Меню", Action: "menu_back"}})
	}
	return Surface{Text: strings.Join(lines, "\n"), Rows: rows}
}

// RenderLegacy keeps already-issued pre-v2 callbacks projectable during their
// short signature lifetime while all new entry points use Render.
func RenderLegacy(snapshot sessioncreation.Snapshot, providers []domain.Provider) Surface {
	draft := snapshot.Draft
	provider := "не выбран"
	if draft.Provider != "" {
		provider = providerName(draft.Provider)
	}
	workdir := draft.Workdir
	if workdir == "" {
		workdir = "не выбрана"
	}
	lines := []string{"Новая сессия", "Компьютер: " + string(draft.ComputerID), "CLI: " + provider, "Рабочая папка: " + workdir}
	if snapshot.ValidationError != "" {
		lines = append(lines, "Ошибка: "+snapshot.ValidationError)
	}
	if snapshot.AwaitingWorkdir {
		lines = append(lines, "Отправьте отдельным сообщением абсолютный путь к рабочей папке.")
		return Surface{Text: strings.Join(lines, "\n"), Rows: [][]Button{{{Label: "Меню", Action: "menu_back"}}}}
	}
	rows := make([][]Button, 0, 4)
	if len(providers) > 1 {
		for _, provider := range providers {
			action := "create_select_codex"
			if provider == domain.ProviderClaude {
				action = "create_select_claude"
			}
			rows = append(rows, []Button{{Label: providerName(provider), Action: action}})
		}
	}
	rows = append(rows, []Button{{Label: "Папка", Action: "create_workdir"}})
	if draft.Workdir != "" && draft.Provider != "" {
		rows = append(rows, []Button{{Label: "Запустить", Action: "create_confirm"}})
	}
	rows = append(rows, []Button{{Label: "Меню", Action: "menu_back"}})
	return Surface{Text: strings.Join(lines, "\n"), Rows: rows}
}

func providerName(provider domain.Provider) string {
	if provider == domain.ProviderClaude {
		return "Claude"
	}
	return "Codex"
}
