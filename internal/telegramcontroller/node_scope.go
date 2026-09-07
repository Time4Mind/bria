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
	if active != "" {
		session, loadErr := controller.sessions.Load(ctx, active)
		if loadErr != nil || (session.Status() != domain.SessionReady && session.Status() != domain.SessionRunning && session.Status() != domain.SessionStopping) {
			// Never expose an awaiting-recovery/failed session as the input target.
			if clearErr := controller.nodes.RestoreActive(ctx, controller.nodes.Current(), ""); clearErr != nil {
				return clearErr
			}
			active = ""
		}
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

func (controller *Controller) setNodeBack(sessionID domain.SessionID) {
	controller.mu.Lock()
	controller.nodeBackSession = sessionID
	controller.mu.Unlock()
}

func (controller *Controller) clearNodeBack() {
	controller.mu.Lock()
	controller.nodeBackSession = ""
	controller.mu.Unlock()
}

func (controller *Controller) nodeListSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	return controller.statusSemanticResult(ctx)
}

func (controller *Controller) statusSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	return controller.statusSemanticResultWithRefresh(ctx, false)
}

func (controller *Controller) statusSemanticResultRefresh(ctx context.Context) (SemanticActionResult, error) {
	return controller.statusSemanticResultWithRefresh(ctx, true)
}

func (controller *Controller) statusSemanticResultWithRefresh(ctx context.Context, refresh bool) (SemanticActionResult, error) {
	nodes, err := controller.nodeInventory(ctx)
	if err != nil {
		return SemanticActionResult{}, fmt.Errorf("list status nodes: %w", err)
	}
	var quotas []telegramstatus.Snapshot
	if controller.quotas != nil {
		if refresh {
			// Quota collection starts provider CLIs and may take tens of seconds.
			// It is auxiliary work and must never hold the callback path hostage:
			// render the cached snapshot now, then refresh it in the controller's
			// lifetime context. A single-flight guard prevents repeated taps from
			// spawning an unbounded set of provider processes.
			controller.startQuotaRefresh()
		}
		quotas, err = controller.quotas.Snapshots(ctx)
		if err != nil {
			if refresh {
				// Refresh is best-effort; preserve the Nodes/Status surface even
				// when the provider quota cache is temporarily unavailable.
				quotas = nil
			} else {
				return SemanticActionResult{}, fmt.Errorf("load provider quotas: %w", err)
			}
		}
	}
	items := make([]telegramstatus.Node, len(nodes))
	for index, node := range nodes {
		items[index] = telegramstatus.Node{ID: node.ID, Name: node.Name, Coordinator: node.Coordinator, Available: node.Available}
	}
	view := telegramnodes.Menu(controller.currentNodeID(), nodes)
	rows := make([][]SemanticButton, len(view), len(view)+1)
	for index, row := range view {
		rows[index] = []SemanticButton{{Label: row[0].Label, Action: SemanticSelectNode, Choice: row[0].Choice}}
	}
	rows = append(rows, []SemanticButton{{Label: "Обновить", Action: SemanticRefreshStatus}, {Label: "Назад", Action: SemanticMenuBack}})
	return SemanticActionResult{Surface: &SemanticSurface{Text: telegramstatus.Render(time.Now(), items, quotas), RichMarkdown: true, Rows: rows}}, nil
}

func (controller *Controller) startQuotaRefresh() {
	refresher, ok := controller.quotas.(interface{ Refresh(context.Context) error })
	if !ok {
		return
	}
	controller.mu.Lock()
	if controller.quotaRefreshInFlight || controller.closed {
		controller.mu.Unlock()
		return
	}
	controller.quotaRefreshInFlight = true
	root := controller.rootContext
	controller.mu.Unlock()
	go func() {
		defer func() {
			controller.mu.Lock()
			controller.quotaRefreshInFlight = false
			controller.mu.Unlock()
		}()
		_ = refresher.Refresh(root)
	}()
}

func (controller *Controller) openSessionsSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	controller.ScheduleStandby()
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
	if native, ok := controller.restoreNativeSurface(active); ok {
		return native, nil
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
	controller.ScheduleStandby()
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
