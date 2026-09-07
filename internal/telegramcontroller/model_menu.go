package telegramcontroller

import (
	"context"

	"bria/internal/domain"
	"bria/internal/settingsport"
)

// ModelCatalog remains a composition compatibility port, not a command menu.
// Native CLI commands obtain their selected value directly from the CLI screen.
type ModelCatalog interface {
	Models(context.Context, domain.ComputerID, domain.Provider) ([]settingsport.Model, error)
}

func (c *Controller) modelNotice(id domain.SessionID, text string) SemanticActionResult {
	button := SemanticButton{Label: "← Сессии", Action: SemanticMenuSessions}
	if id != "" {
		button = SemanticButton{Label: "← К сессии", Action: SemanticSelect, SessionID: id}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: text, Rows: [][]SemanticButton{{button}}}}
}
