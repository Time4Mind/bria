package telegramcontroller

import (
	"context"
	"fmt"
	"html"
	"sort"
	"strings"

	"bria/internal/domain"
	"bria/internal/telegramsessions"
)

const archivePageSize = 6

func (controller *Controller) archiveSemanticResult(ctx context.Context, requestedPage int) (SemanticActionResult, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	currentNode := controller.currentNodeID()
	labelsByID := telegramsessions.Labels(sessions, currentNode)
	archived := make([]domain.Session, 0, len(sessions))
	for _, session := range sessions {
		if session.ComputerID() == currentNode && session.Status() == domain.SessionArchived {
			archived = append(archived, session)
		}
	}
	sort.Slice(archived, func(i, j int) bool {
		if changed := archived[i].StateChangedAt().Compare(archived[j].StateChangedAt()); changed != 0 {
			return changed > 0
		}
		return archived[i].ID() < archived[j].ID()
	})

	pages := max(1, (len(archived)+archivePageSize-1)/archivePageSize)
	page := min(max(1, requestedPage), pages)
	start := (page - 1) * archivePageSize
	end := min(len(archived), start+archivePageSize)
	visible := archived[start:end]

	var text strings.Builder
	text.WriteString("Архив")
	if len(visible) > 0 {
		text.WriteString("\n\n\u00a0\n\n| Name | Description |\n|---|---|")
		for index, session := range visible {
			label := fmt.Sprintf("%d. %s", start+index+1, labelsByID[session.ID()])
			lines := controller.archivePromptDescription(ctx, session.ID())
			for i := range lines {
				lines[i] = "· " + archiveTableCell(lines[i])
			}
			fmt.Fprintf(&text, "\n| %s | %s |", archiveTableCell(label), strings.Join(lines, "<br>"))
		}
	}

	rows := make([][]SemanticButton, 0, 5)
	for index, session := range visible {
		if index%2 == 0 {
			rows = append(rows, make([]SemanticButton, 0, 2))
		}
		label := fmt.Sprintf("%d. %s", start+index+1, labelsByID[session.ID()])
		rows[len(rows)-1] = append(rows[len(rows)-1], SemanticButton{Label: label, Action: SemanticResume, SessionID: session.ID()})
	}
	pager := []SemanticButton{
		{Label: "◀", Action: SemanticMenuArchive, Choice: max(1, page-1)},
		{Label: fmt.Sprintf("%d/%d", page, pages), Action: SemanticMenuArchive, Choice: 1},
		{Label: "▶", Action: SemanticMenuArchive, Choice: min(pages, page+1)},
	}
	rows = append(rows, pager, []SemanticButton{{Label: "Меню", Action: SemanticMenuBack}})
	return SemanticActionResult{Surface: &SemanticSurface{Text: text.String(), RichMarkdown: len(visible) > 0, Rows: rows}}, nil
}

func archiveTableCell(value string) string {
	value = strings.NewReplacer("\r", " ", "\n", " ").Replace(value)
	return strings.NewReplacer("\\", "\\\\", "|", "\\|", "`", "\\`", "*", "\\*", "_", "\\_",
		"~", "\\~", "[", "\\[", "]", "\\]").Replace(html.EscapeString(value))
}
