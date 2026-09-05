package telegramcontroller

import (
	"context"
	"errors"
	"sort"

	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessioncreation"
	"bria/internal/settingsport"
	"bria/internal/telegramcreationview"
	"bria/internal/telegramsessions"
	"bria/internal/telegramsettingsview"
)

const (
	creationPreferenceProvider = "provider"
	creationPreferenceWorkdir  = "workdir"
)

func (controller *Controller) setCreationPreferenceMode(mode string) {
	controller.mu.Lock()
	controller.creationPreferenceMode = mode
	controller.mu.Unlock()
}

func (controller *Controller) currentCreationPreferenceMode() string {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return controller.creationPreferenceMode
}

func (controller *Controller) beginCreationPreferenceV2(ctx context.Context, mode string) (SemanticActionResult, error) {
	if _, ok := controller.settings.(settingsport.CreationPreferences); !ok {
		return SemanticActionResult{}, errors.New("session creation settings are not configured")
	}
	computers, defaults, err := controller.creationStateV2(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	switch mode {
	case creationPreferenceProvider:
		defaults.Providers = nil
	case creationPreferenceWorkdir:
		defaults.Workdirs = nil
	default:
		return SemanticActionResult{}, errors.New("unsupported session creation preference")
	}
	defaults.ArchiveRecommendations = false
	controller.setCreationPreferenceMode(mode)
	snapshot := controller.createFlow.BeginV2(computers, defaults)
	return controller.advanceCreationPreferenceV2(ctx, snapshot)
}

func (controller *Controller) advanceCreationPreferenceV2(ctx context.Context, snapshot sessioncreation.Snapshot) (SemanticActionResult, error) {
	mode := controller.currentCreationPreferenceMode()
	if mode == creationPreferenceProvider && snapshot.Step == sessioncreation.StepDirectory {
		preferences := controller.settings.(settingsport.CreationPreferences)
		if err := preferences.SetDefaultProvider(ctx, snapshot.Draft.ComputerID, snapshot.Draft.Provider); err != nil {
			return SemanticActionResult{}, err
		}
		controller.cancelCreateDraft()
		return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryCreation)
	}
	return controller.advanceCreateV2(ctx, snapshot, 0)
}

func (controller *Controller) clearCreationPreferencesV2(ctx context.Context) (SemanticActionResult, error) {
	preferences, ok := controller.settings.(settingsport.CreationPreferences)
	if !ok {
		return SemanticActionResult{}, errors.New("session creation settings are not configured")
	}
	computerID := controller.currentNodeID()
	if err := preferences.ClearDefaultProvider(ctx, computerID); err != nil {
		return SemanticActionResult{}, err
	}
	if err := preferences.ClearDefaultWorkdir(ctx, computerID); err != nil {
		return SemanticActionResult{}, err
	}
	return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryCreation)
}

func (controller *Controller) beginNewSessionV2(ctx context.Context, updateID int64) (SemanticActionResult, error) {
	computers, defaults, err := controller.creationStateV2(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	if len(computers) == 0 {
		controller.cancelCreateDraft()
		return controller.nodeListSemanticResult(ctx)
	}
	snapshot := controller.createFlow.BeginV2(computers, defaults)
	return controller.advanceCreateV2(ctx, snapshot, updateID)
}

func (controller *Controller) currentCreateDraftSurfaceV2(ctx context.Context) (*SemanticSurface, error) {
	snapshot, open, err := controller.currentCreateSnapshotV2(ctx)
	if err != nil {
		return nil, err
	}
	if !open {
		result, beginErr := controller.beginNewSessionV2(ctx, 0)
		return result.Surface, beginErr
	}
	result, err := controller.advanceCreateV2(ctx, snapshot, 0)
	return result.Surface, err
}

func (controller *Controller) creationStateV2(ctx context.Context) ([]sessioncreation.Computer, sessioncreation.Defaults, error) {
	defaults := sessioncreation.Defaults{}
	if controller.settings != nil {
		current, err := controller.settings.Snapshot(ctx)
		if err != nil {
			return nil, defaults, err
		}
		defaults.Providers = current.DefaultProviders
		defaults.Workdirs = current.DefaultWorkdirs
		defaults.ArchiveRecommendations = current.ArchiveRecommendations
	}
	if controller.creationEnvironment != nil {
		computers, err := controller.creationEnvironment.AvailableComputers(ctx)
		if err != nil {
			return nil, defaults, err
		}
		selected := controller.currentNodeID()
		for _, computer := range computers {
			if computer.ID == selected {
				return []sessioncreation.Computer{computer}, defaults, nil
			}
		}
		return nil, defaults, nil
	}
	capabilities, err := controller.providerCapabilitiesV2(ctx)
	if err != nil {
		return nil, defaults, err
	}
	if controller.currentNodeID() != controller.localComputerID {
		return nil, defaults, nil
	}
	return []sessioncreation.Computer{{
		ID: controller.localComputerID, Name: string(controller.localComputerID),
		Capabilities: capabilities,
	}}, defaults, nil
}

func (controller *Controller) providerCapabilitiesV2(ctx context.Context) ([]sessioncreation.ProviderCapability, error) {
	if controller.providerPreferences == nil {
		return []sessioncreation.ProviderCapability{
			{Provider: domain.ProviderCodex, Installed: true, Enabled: true},
			{Provider: domain.ProviderClaude, Installed: true, Enabled: true},
		}, nil
	}
	preferences, err := controller.providerPreferences.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	result := make([]sessioncreation.ProviderCapability, 0, len(preferences))
	for _, preference := range preferences {
		result = append(result, sessioncreation.ProviderCapability{
			Provider: preference.Provider, Installed: preference.Configured,
			Enabled: preference.Configured && preference.Enabled,
		})
	}
	return result, nil
}

func (controller *Controller) currentCreateSnapshotV2(ctx context.Context) (sessioncreation.Snapshot, bool, error) {
	computers, defaults, err := controller.creationStateV2(ctx)
	if err != nil {
		return sessioncreation.Snapshot{}, false, err
	}
	snapshot, open := controller.createFlow.CurrentV2(computers, defaults)
	return snapshot, open, nil
}

func (controller *Controller) advanceCreateV2(ctx context.Context, snapshot sessioncreation.Snapshot, updateID int64) (SemanticActionResult, error) {
	if snapshot.Step == sessioncreation.StepUnavailable {
		controller.cancelCreateDraft()
		return controller.nodeListSemanticResult(ctx)
	}
	if snapshot.Step == sessioncreation.StepDirectory && snapshot.CurrentDirectory == "" {
		if defaultWorkdir := controller.createFlow.DefaultWorkdir(); defaultWorkdir != "" {
			if directories, err := controller.browseCreationDirectoryV2(ctx, snapshot.Draft.ComputerID, defaultWorkdir); err == nil {
				if err := controller.createFlow.SetDirectoryListing(defaultWorkdir, directories); err != nil {
					return SemanticActionResult{}, err
				}
				if err := controller.createFlow.SelectWorkdir(defaultWorkdir); err != nil {
					return SemanticActionResult{}, err
				}
				return controller.finishDirectorySelectionV2(ctx, updateID)
			}
			controller.createFlow.SetDirectoryError("папка по умолчанию недоступна; выберите другую")
		}
		if home, err := controller.creationHomeV2(ctx, snapshot.Draft.ComputerID); err == nil {
			if directories, browseErr := controller.browseCreationDirectoryV2(ctx, snapshot.Draft.ComputerID, home); browseErr == nil {
				if err := controller.createFlow.SetDirectoryListing(home, directories); err != nil {
					return SemanticActionResult{}, err
				}
				snapshot, _, _ = controller.currentCreateSnapshotV2(ctx)
				return SemanticActionResult{Surface: renderCreateSurfaceV2(snapshot)}, nil
			}
		}
		roots, err := controller.creationRootsV2(ctx, snapshot.Draft.ComputerID)
		if err != nil {
			return SemanticActionResult{}, err
		}
		if err := controller.createFlow.SetDirectoryListing("", roots); err != nil {
			return SemanticActionResult{}, err
		}
		snapshot, _, _ = controller.currentCreateSnapshotV2(ctx)
	}
	if snapshot.Step == sessioncreation.StepReady && updateID > 0 {
		return controller.confirmCreateDraftV2(ctx, updateID)
	}
	return SemanticActionResult{Surface: renderCreateSurfaceV2(snapshot)}, nil
}

func (controller *Controller) creationHomeV2(ctx context.Context, computerID domain.ComputerID) (string, error) {
	if controller.creationEnvironment != nil {
		return controller.creationEnvironment.Home(ctx, computerID)
	}
	browser, err := sessioncreation.NewLocalBrowser(controller.localComputerID, nil)
	if err != nil {
		return "", err
	}
	return browser.Home(ctx, computerID)
}

func (controller *Controller) creationRootsV2(ctx context.Context, computerID domain.ComputerID) ([]sessioncreation.Directory, error) {
	var directories []sessioncreation.Directory
	var err error
	if controller.creationEnvironment != nil {
		directories, err = controller.creationEnvironment.Roots(ctx, computerID)
	} else {
		var browser *sessioncreation.LocalBrowser
		browser, err = sessioncreation.NewLocalBrowser(controller.localComputerID, nil)
		if err == nil {
			directories, err = browser.Roots(ctx, computerID)
		}
	}
	if err != nil {
		return nil, err
	}
	return controller.limitCreationChoicesV2(ctx, directories), nil
}

func (controller *Controller) browseCreationDirectoryV2(ctx context.Context, computerID domain.ComputerID, path string) ([]sessioncreation.Directory, error) {
	var directories []sessioncreation.Directory
	var err error
	if controller.creationEnvironment != nil {
		directories, err = controller.creationEnvironment.Browse(ctx, computerID, path)
	} else {
		var browser *sessioncreation.LocalBrowser
		browser, err = sessioncreation.NewLocalBrowser(controller.localComputerID, nil)
		if err == nil {
			directories, err = browser.Browse(ctx, computerID, path)
		}
	}
	if err != nil {
		return nil, err
	}
	return controller.limitCreationChoicesV2(ctx, directories), nil
}

func (controller *Controller) limitCreationChoicesV2(ctx context.Context, items []sessioncreation.Directory) []sessioncreation.Directory {
	pages := 64
	if controller.settings != nil {
		if current, err := controller.settings.Snapshot(ctx); err == nil && current.CardPageLimit > 0 {
			pages = current.CardPageLimit
		}
	}
	limit := pages * sessioncreation.ChoicesPerPage
	if len(items) > limit {
		return append([]sessioncreation.Directory(nil), items[:limit]...)
	}
	return items
}

func (controller *Controller) creationParentV2(ctx context.Context, computerID domain.ComputerID, path string) (string, bool) {
	if controller.creationEnvironment != nil {
		return controller.creationEnvironment.Parent(ctx, computerID, path)
	}
	browser, err := sessioncreation.NewLocalBrowser(controller.localComputerID, nil)
	if err != nil {
		return "", false
	}
	return browser.Parent(path)
}

func (controller *Controller) selectCreateProviderV2(ctx context.Context, provider domain.Provider) error {
	snapshot, open, err := controller.currentCreateSnapshotV2(ctx)
	if err != nil {
		return err
	}
	if !open {
		return errProviderUnavailable
	}
	enabled, installed := false, false
	for _, capability := range snapshot.Providers {
		if capability.Provider == provider {
			enabled, installed = capability.Enabled, capability.Installed
		}
	}
	if !installed {
		return errProviderUnavailable
	}
	if !enabled {
		if err := controller.toggleNodeProvider(ctx, snapshot.Draft.ComputerID, provider); err != nil {
			return err
		}
	}
	computers, defaults, err := controller.creationStateV2(ctx)
	if err != nil {
		return err
	}
	controller.createFlow.CurrentV2(computers, defaults)
	if controller.currentCreationPreferenceMode() == creationPreferenceWorkdir {
		defaults.Workdirs = nil
	}
	if err := controller.createFlow.SelectProviderV2(provider, defaults); err != nil {
		return errProviderUnavailable
	}
	return nil
}

func (controller *Controller) handleCreateChoiceV2(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	snapshot, open, err := controller.currentCreateSnapshotV2(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	if !open {
		return controller.ProjectCurrent(ctx, "")
	}
	switch snapshot.Step {
	case sessioncreation.StepComputer:
		_, defaults, stateErr := controller.creationStateV2(ctx)
		if stateErr != nil {
			return SemanticActionResult{}, stateErr
		}
		switch controller.currentCreationPreferenceMode() {
		case creationPreferenceProvider:
			defaults.Providers = nil
		case creationPreferenceWorkdir:
			defaults.Workdirs = nil
		}
		if err := controller.createFlow.SelectComputer(action.Choice, defaults); err != nil {
			return SemanticActionResult{}, err
		}
		next, _, _ := controller.currentCreateSnapshotV2(ctx)
		if controller.currentCreationPreferenceMode() != "" {
			return controller.advanceCreationPreferenceV2(ctx, next)
		}
		return controller.advanceCreateV2(ctx, next, action.UpdateID)
	case sessioncreation.StepDirectory:
		directory, err := controller.createFlow.DirectoryChoice(action.Choice)
		if err != nil {
			return SemanticActionResult{}, err
		}
		children, err := controller.browseCreationDirectoryV2(ctx, snapshot.Draft.ComputerID, directory.Path)
		if err != nil {
			controller.createFlow.SetDirectoryError("папка недоступна; список обновлён")
			children, _ = controller.browseCreationDirectoryV2(ctx, snapshot.Draft.ComputerID, snapshot.CurrentDirectory)
			_ = controller.createFlow.SetDirectoryListing(snapshot.CurrentDirectory, children)
		} else if err := controller.createFlow.SetDirectoryListing(directory.Path, children); err != nil {
			return SemanticActionResult{}, err
		}
		next, _, _ := controller.currentCreateSnapshotV2(ctx)
		return SemanticActionResult{Surface: renderCreateSurfaceV2(next)}, nil
	case sessioncreation.StepRecommendation:
		recommendation, err := controller.createFlow.RecommendationChoice(action.Choice)
		if err != nil {
			return SemanticActionResult{}, err
		}
		controller.createFlow.Cancel()
		decision, err := controller.ResumeArchived(ctx, recommendation.SessionID)
		if err != nil {
			return SemanticActionResult{}, err
		}
		card, cardErr := controller.semanticCard(ctx, recommendation.SessionID, true)
		return SemanticActionResult{Decision: decision, Card: &card}, cardErr
	default:
		return SemanticActionResult{Surface: renderCreateSurfaceV2(snapshot)}, nil
	}
}

func (controller *Controller) handleCreateNavigationV2(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	switch action.Kind {
	case SemanticCreatePrevious:
		return SemanticActionResult{Surface: renderCreateSurfaceV2(controller.createFlow.Page(-1, false))}, nil
	case SemanticCreateFirst:
		return SemanticActionResult{Surface: renderCreateSurfaceV2(controller.createFlow.Page(0, true))}, nil
	case SemanticCreateNext:
		return SemanticActionResult{Surface: renderCreateSurfaceV2(controller.createFlow.Page(1, false))}, nil
	}
	snapshot, open, err := controller.currentCreateSnapshotV2(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	if !open {
		return controller.ProjectCurrent(ctx, "")
	}
	switch action.Kind {
	case SemanticCreateUp:
		if snapshot.Step != sessioncreation.StepDirectory {
			return SemanticActionResult{Surface: renderCreateSurfaceV2(snapshot)}, nil
		}
		if parent, ok := controller.creationParentV2(ctx, snapshot.Draft.ComputerID, snapshot.CurrentDirectory); ok {
			children, browseErr := controller.browseCreationDirectoryV2(ctx, snapshot.Draft.ComputerID, parent)
			if browseErr != nil {
				return SemanticActionResult{}, browseErr
			}
			_ = controller.createFlow.SetDirectoryListing(parent, children)
		} else {
			roots, rootsErr := controller.creationRootsV2(ctx, snapshot.Draft.ComputerID)
			if rootsErr != nil {
				return SemanticActionResult{}, rootsErr
			}
			_ = controller.createFlow.SetDirectoryListing("", roots)
		}
	case SemanticCreatePick:
		if snapshot.Step != sessioncreation.StepDirectory || snapshot.CurrentDirectory == "" {
			return SemanticActionResult{Surface: renderCreateSurfaceV2(snapshot)}, nil
		}
		if err := controller.createFlow.SelectWorkdir(snapshot.CurrentDirectory); err != nil {
			return SemanticActionResult{}, err
		}
		if controller.currentCreationPreferenceMode() == creationPreferenceWorkdir {
			preferences := controller.settings.(settingsport.CreationPreferences)
			if err := preferences.SetDefaultWorkdir(ctx, snapshot.Draft.ComputerID, snapshot.CurrentDirectory); err != nil {
				return SemanticActionResult{}, err
			}
			controller.cancelCreateDraft()
			return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryCreation)
		}
		return controller.finishDirectorySelectionV2(ctx, action.UpdateID)
	case SemanticCreateDirectoryNew:
		if err := controller.createFlow.BeginDirectoryName(); err != nil {
			return SemanticActionResult{}, err
		}
	case SemanticCreateBack:
		back, ok := controller.createFlow.Back()
		if !ok {
			controller.createFlow.Cancel()
			if controller.currentCreationPreferenceMode() != "" {
				controller.setCreationPreferenceMode("")
				return controller.settingsCategorySemanticResult(ctx, telegramsettingsview.CategoryCreation)
			}
			return controller.sessionListSemanticResult(ctx)
		}
		if controller.currentCreationPreferenceMode() != "" {
			return controller.advanceCreationPreferenceV2(ctx, back)
		}
		return controller.advanceCreateV2(ctx, back, 0)
	case SemanticCreateFresh:
		if snapshot.Step == sessioncreation.StepRecommendation {
			if err := controller.createFlow.SkipRecommendations(); err != nil {
				return SemanticActionResult{}, err
			}
		}
		return controller.confirmCreateDraftV2(ctx, action.UpdateID)
	}
	next, _, _ := controller.currentCreateSnapshotV2(ctx)
	return SemanticActionResult{Surface: renderCreateSurfaceV2(next)}, nil
}

func (controller *Controller) consumeCreateDirectoryNameV2(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool) {
	name, consumed := controller.createFlow.ConsumeDirectoryName(update.Text, update.MediaKind != "" || update.Caption != "")
	if !consumed {
		return coordinator.Decision{}, false
	}
	if name == "" {
		return controller.status("Нужно отдельное текстовое имя дочерней папки."), true
	}
	snapshot, open, err := controller.currentCreateSnapshotV2(ctx)
	if err != nil || !open {
		return controller.status("Создание сессии уже завершено."), true
	}
	parent := controller.createFlow.CurrentDirectory()
	var created string
	if controller.creationEnvironment != nil {
		created, err = controller.creationEnvironment.CreateChild(ctx, snapshot.Draft.ComputerID, parent, name)
	} else {
		browser, browserErr := sessioncreation.NewLocalBrowser(controller.localComputerID, nil)
		if browserErr != nil {
			err = browserErr
		} else {
			created, err = browser.CreateChild(ctx, snapshot.Draft.ComputerID, parent, name)
		}
	}
	if err != nil {
		controller.createFlow.SetDirectoryError("не удалось создать папку")
		return controller.status("Не удалось создать папку."), true
	}
	children, err := controller.browseCreationDirectoryV2(ctx, snapshot.Draft.ComputerID, created)
	if err != nil || controller.createFlow.SetDirectoryListing(created, children) != nil {
		return controller.status("Не удалось открыть созданную папку."), true
	}
	return controller.status("Папка создана."), true
}

func (controller *Controller) finishDirectorySelectionV2(ctx context.Context, updateID int64) (SemanticActionResult, error) {
	snapshot, open, err := controller.currentCreateSnapshotV2(ctx)
	if err != nil || !open {
		return SemanticActionResult{}, errors.New("session creation flow is not open")
	}
	if snapshot.ArchiveRecommendations {
		sessions, err := controller.sessions.List(ctx)
		if err != nil {
			return SemanticActionResult{}, err
		}
		sort.SliceStable(sessions, func(i, j int) bool {
			return sessions[i].StateChangedAt().After(sessions[j].StateChangedAt())
		})
		items := make([]sessioncreation.Recommendation, 0)
		maxItems := 64 * sessioncreation.ChoicesPerPage
		if controller.settings != nil {
			if current, snapshotErr := controller.settings.Snapshot(ctx); snapshotErr == nil && current.CardPageLimit > 0 {
				maxItems = current.CardPageLimit * sessioncreation.ChoicesPerPage
			}
		}
		for _, session := range sessions {
			if session.Status() != domain.SessionArchived || session.ComputerID() != snapshot.Draft.ComputerID ||
				session.Provider() != snapshot.Draft.Provider || session.Workdir() != snapshot.Draft.Workdir {
				continue
			}
			items = append(items, sessioncreation.Recommendation{
				SessionID: session.ID(),
				Label: authorizationProviderName(session.Provider()) + " " + telegramsessions.ShortID(session.ID()) + " · " +
					session.StateChangedAt().Local().Format("02.01 15:04"),
			})
			if len(items) == maxItems {
				break
			}
		}
		if err := controller.createFlow.SetRecommendations(items); err != nil {
			return SemanticActionResult{}, err
		}
	} else if err := controller.createFlow.SkipRecommendations(); err != nil {
		return SemanticActionResult{}, err
	}
	snapshot, _, _ = controller.currentCreateSnapshotV2(ctx)
	if snapshot.Step == sessioncreation.StepReady && updateID > 0 {
		return controller.confirmCreateDraftV2(ctx, updateID)
	}
	return SemanticActionResult{Surface: renderCreateSurfaceV2(snapshot)}, nil
}

func (controller *Controller) confirmCreateDraftV2(ctx context.Context, updateID int64) (SemanticActionResult, error) {
	draft, ready := controller.createFlow.Ready()
	if !ready || updateID <= 0 {
		return SemanticActionResult{}, errors.New("session creation draft is not ready")
	}
	computers, defaults, err := controller.creationStateV2(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	snapshot, _ := controller.createFlow.CurrentV2(computers, defaults)
	if snapshot.Step != sessioncreation.StepReady {
		return controller.advanceCreateV2(ctx, snapshot, updateID)
	}
	decision, err := controller.create(ctx, updateID, draft.ComputerID, draft.Provider, draft.Workdir)
	if errors.Is(err, errProviderUnavailable) {
		return SemanticActionResult{Surface: unavailableNewSessionSurface()}, nil
	}
	if err != nil {
		return SemanticActionResult{}, err
	}
	controller.cancelCreateDraft()
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	card, cardErr := controller.semanticCard(ctx, active, true)
	return SemanticActionResult{Decision: decision, Card: &card}, cardErr
}

func renderCreateSurfaceV2(snapshot sessioncreation.Snapshot) *SemanticSurface {
	return semanticCreationSurface(telegramcreationview.Render(snapshot))
}

func semanticCreationSurface(view telegramcreationview.Surface) *SemanticSurface {
	rows := make([][]SemanticButton, len(view.Rows))
	for rowIndex, row := range view.Rows {
		rows[rowIndex] = make([]SemanticButton, len(row))
		for buttonIndex, button := range row {
			rows[rowIndex][buttonIndex] = SemanticButton{Label: button.Label, Action: SemanticActionKind(button.Action), Choice: button.Choice}
		}
	}
	return &SemanticSurface{Text: view.Text, Rows: rows}
}
