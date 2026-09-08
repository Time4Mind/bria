package telegramcontroller

import (
	"bria/internal/domain"
	"bria/internal/telegramcontrolport"
)

// ModelCatalog remains a composition compatibility port, not a command menu.
// Native CLI commands obtain their selected value directly from the CLI screen.
type ModelCatalog = telegramcontrolport.ModelCatalog

func (c *Controller) modelNotice(id domain.SessionID, text string) SemanticActionResult {
	button := SemanticButton{Label: "← Сессии", Action: SemanticMenuSessions}
	if id != "" {
		button = SemanticButton{Label: "← К сессии", Action: SemanticSelect, SessionID: id}
	}
	return SemanticActionResult{Surface: &SemanticSurface{Text: text, Rows: [][]SemanticButton{{button}}}}
}
