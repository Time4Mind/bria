package telegramcontroller

import (
	"context"
	"errors"
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
		if loadErr != nil || !restorableForegroundStatus(session.Status()) {
			// Foreground viewing and input readiness are separate capabilities.
			if clearErr := controller.nodes.RestoreActive(ctx, controller.nodes.Current(), ""); clearErr != nil {
				return clearErr
			}
			active = ""
		} else if acceptsDurableInput(session) {
			// A process restart loses the ephemeral async-creation handle, but the
			// persisted Starting/Resuming session must remain the FIFO target until
			// supervision commits Ready and wakes its durable input.
			controller.mu.Lock()
			controller.pending[session.ID()] = session
			controller.mu.Unlock()
		}
	}
	controller.mu.Lock()
	controller.active = active
	controller.mu.Unlock()
	return nil
}

func restorableForegroundStatus(status domain.SessionStatus) bool {
	return status == domain.SessionStarting || status == domain.SessionResuming ||
		status == domain.SessionReady || status == domain.SessionRunning ||
		status == domain.SessionStopping || status == domain.SessionAwaitingRecovery
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

func (controller *Controller) nodeBackButton() SemanticButton {
	controller.mu.Lock()
	sessionID := controller.nodeBackSession
	controller.mu.Unlock()
	if sessionID != "" {
		// Bind the return target into the signed callback itself. The button must
		// remain correct after a refresh or process restart instead of relying on
		// the controller's ephemeral navigation memory when it is pressed.
		return SemanticButton{Label: "Назад", Action: SemanticSelect, SessionID: sessionID}
	}
	return SemanticButton{Label: "Назад", Action: SemanticMenuBack}
}

func (controller *Controller) nodeListSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	return controller.statusSemanticResult(ctx)
}

func (controller *Controller) statusSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	return controller.statusSemanticResultWithRefresh(ctx, false)
}

func (controller *Controller) statusSemanticResultRefresh(ctx context.Context) (SemanticActionResult, error) {
	// The callback must publish this cached projection first. The runtime starts
	// the provider collection only after that Telegram edit is committed.
	return controller.statusSemanticResultWithRefresh(ctx, true)
}

func (controller *Controller) statusSemanticResultWithRefresh(ctx context.Context, refresh bool) (SemanticActionResult, error) {
	nodes, err := controller.nodeInventory(ctx)
	if err != nil {
		return SemanticActionResult{}, fmt.Errorf("list status nodes: %w", err)
	}
	var quotas []telegramstatus.Snapshot
	if controller.quotas != nil {
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
	rows = append(rows, []SemanticButton{{Label: "Обновить", Action: SemanticRefreshStatus}, controller.nodeBackButton()})
	return SemanticActionResult{Surface: &SemanticSurface{Text: telegramstatus.Render(time.Now(), items, quotas), RichMarkdown: true, Rows: rows}}, nil
}

// RefreshStatus synchronously collects provider quotas and projects the fresh
// Status surface. The runtime invokes it outside the Telegram callback path.
func (controller *Controller) RefreshStatus(ctx context.Context) (SemanticActionResult, error) {
	if ctx == nil {
		return SemanticActionResult{}, errors.New("status refresh context is required")
	}
	refreshContext, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(controller.rootContext, cancel)
	defer func() {
		stop()
		cancel()
	}()
	refresher, ok := controller.quotas.(interface{ Refresh(context.Context) error })
	if ok {
		if err := refresher.Refresh(refreshContext); err != nil {
			return SemanticActionResult{}, err
		}
	}
	return controller.statusSemanticResult(refreshContext)
}

func (controller *Controller) openSessionsSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	controller.ScheduleStandby()
	active, err := controller.ensureCurrentActive(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
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
	controller.selectionMu.Lock()
	current := controller.currentNodeID()
	active, selected, err := controller.nodes.Select(ctx, nodeID)
	if err != nil {
		controller.selectionMu.Unlock()
		return SemanticActionResult{}, err
	}
	if current == nodeID || !selected {
		controller.selectionMu.Unlock()
		return controller.nodeListSemanticResult(ctx)
	}
	controller.mu.Lock()
	controller.active = active
	controller.mu.Unlock()
	controller.selectionMu.Unlock()
	controller.clearNodeBack()
	controller.ScheduleStandby()
	if active != "" {
		card, err := controller.semanticCard(ctx, active, true)
		return SemanticActionResult{Card: &card}, err
	}
	return controller.sessionListSemanticResult(ctx)
}

func (controller *Controller) ensureCurrentActive(ctx context.Context) (domain.SessionID, error) {
	controller.selectionMu.Lock()
	defer controller.selectionMu.Unlock()
	active, err := controller.nodes.EnsureActive(ctx, controller.currentNodeID())
	if err == nil {
		controller.mu.Lock()
		controller.active = active
		controller.mu.Unlock()
	}
	return active, err
}

func (controller *Controller) setCurrentNodeActive(ctx context.Context, session domain.Session) error {
	controller.selectionMu.Lock()
	defer controller.selectionMu.Unlock()
	return controller.setCurrentNodeActiveLocked(ctx, session)
}

func (controller *Controller) setCurrentNodeActiveLocked(ctx context.Context, session domain.Session) error {
	if err := controller.nodes.SetActive(ctx, session); err != nil {
		return err
	}
	controller.mu.Lock()
	controller.active = session.ID()
	controller.mu.Unlock()
	return nil
}
