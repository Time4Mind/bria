package telegramcontroller

import (
	"context"
	"fmt"
	"time"

	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/telegramnodes"
	"bria/internal/telegramstatus"
)

func (controller *Controller) restoreNodeSelection(ctx context.Context) error {
	active, err := controller.nodes.Restore(ctx)
	if err != nil {
		return err
	}
	controller.mu.Lock()
	controller.active = active
	controller.mu.Unlock()
	return nil
}

func (controller *Controller) nodeAvailable(ctx context.Context, nodeID domain.ComputerID) bool {
	return controller.nodes.Available(ctx, nodeID)
}

func (controller *Controller) nodeInventory(ctx context.Context) ([]sessioncreation.Computer, error) {
	return controller.nodes.Inventory(ctx)
}

func (controller *Controller) currentNodeID() domain.ComputerID {
	return controller.nodes.Current()
}

func (controller *Controller) nodeListSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	nodes, err := controller.nodeInventory(ctx)
	if err != nil {
		return SemanticActionResult{}, fmt.Errorf("list nodes: %w", err)
	}
	view := telegramnodes.Menu(controller.currentNodeID(), nodes)
	rows := make([][]SemanticButton, len(view), len(view)+1)
	for index, row := range view {
		rows[index] = []SemanticButton{{Label: row[0].Label, Action: SemanticSelectNode, Choice: row[0].Choice}}
	}
	rows = append(rows, []SemanticButton{{Label: "≡ Меню", Action: SemanticMenuBack}})
	return SemanticActionResult{Surface: &SemanticSurface{Text: "Ноды", Rows: rows}}, nil
}

func (controller *Controller) statusSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	nodes, err := controller.nodeInventory(ctx)
	if err != nil {
		return SemanticActionResult{}, fmt.Errorf("list status nodes: %w", err)
	}
	var quotas []telegramstatus.Snapshot
	if controller.quotas != nil {
		quotas, err = controller.quotas.Snapshots(ctx)
		if err != nil {
			return SemanticActionResult{}, fmt.Errorf("load provider quotas: %w", err)
		}
	}
	items := make([]telegramstatus.Node, len(nodes))
	for index, node := range nodes {
		items[index] = telegramstatus.Node{ID: node.ID, Name: node.Name, Coordinator: node.Coordinator, Available: node.Available}
	}
	result, err := controller.nodeListSemanticResult(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	result.Surface.Text = telegramstatus.Render(time.Now(), items, quotas)
	result.Surface.RichMarkdown = true
	return result, nil
}

func (controller *Controller) openSessionsSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	nodeID := controller.currentNodeID()
	active, err := controller.nodes.EnsureActive(ctx, nodeID)
	if err != nil {
		return SemanticActionResult{}, err
	}
	controller.mu.Lock()
	controller.active = active
	controller.mu.Unlock()
	if active == "" {
		return controller.sessionListSemanticResult(ctx)
	}
	card, err := controller.semanticCard(ctx, active, true)
	return SemanticActionResult{Card: &card}, err
}

func (controller *Controller) selectNodeSemantic(ctx context.Context, choice int) (SemanticActionResult, error) {
	nodes, err := controller.nodeInventory(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	if choice < 1 || choice > len(nodes) || !nodes[choice-1].Available {
		return controller.nodeListSemanticResult(ctx)
	}
	nodeID := nodes[choice-1].ID
	controller.cancelCreateDraft()
	current := controller.currentNodeID()
	active, selected, err := controller.nodes.Select(ctx, nodeID)
	if err != nil {
		return SemanticActionResult{}, err
	}
	if current == nodeID || !selected {
		return controller.nodeListSemanticResult(ctx)
	}
	controller.mu.Lock()
	controller.active = active
	controller.mu.Unlock()
	if active != "" {
		card, err := controller.semanticCard(ctx, active, true)
		return SemanticActionResult{Card: &card}, err
	}
	return controller.sessionListSemanticResult(ctx)
}

func (controller *Controller) setCurrentNodeActive(ctx context.Context, session domain.Session) error {
	if err := controller.nodes.SetActive(ctx, session); err != nil {
		return err
	}
	controller.mu.Lock()
	controller.active = session.ID()
	controller.mu.Unlock()
	return nil
}
