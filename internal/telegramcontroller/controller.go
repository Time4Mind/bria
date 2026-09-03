package telegramcontroller

import (
	"bria/internal/app"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/settingsport"
	"bria/internal/telegramsettings"
	"bria/internal/turnprocessing"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"
)

const defaultQueueLimit = 16

var errProviderUnavailable = errors.New("provider is not configured and enabled")

type SessionCreator interface {
	Create(context.Context, app.ConfirmedSessionIntent) (app.CreateSessionResult, error)
}
type PendingSessionStart struct {
	Session domain.Session
	Outcome <-chan SessionStartOutcome
}
type SessionStartOutcome struct {
	Session    domain.Session
	Replayed   bool
	StartError error
	Err        error
}
type AsyncSessionCreator interface {
	BeginCreate(context.Context, app.ConfirmedSessionIntent) (PendingSessionStart, error)
}
type SessionStore interface {
	List(context.Context) ([]domain.Session, error)
	Load(context.Context, domain.SessionID) (domain.Session, error)
}
type Notifier interface {
	Notify(context.Context, Notification) error
}
type DeliveryState string

const DeliveryUnknown DeliveryState = "unknown"

type NotificationFailure struct {
	SessionID       domain.SessionID
	Kind            NotificationKind
	State           DeliveryState
	DurablyRecorded bool
}
type OutputFailureRecorder interface {
	RecordNotificationFailure(context.Context, NotificationFailure) error
}
type ActiveSessionStore interface {
	SetActiveSession(context.Context, domain.SessionID) error
}
type CardCarrierStore interface {
	SetCardCarrier(context.Context, domain.SessionID, int64, int64) error
}
type CardPageStore interface {
	SetCardPage(context.Context, domain.SessionID, int, int, string, bool) error
}
type CardHistoryStore interface {
	AppendCardHistory(context.Context, domain.SessionID, string) error
	LoadCardHistory(context.Context, domain.SessionID) ([]string, error)
}
type ActiveSessionLoader interface {
	LoadActiveSession(context.Context) (domain.SessionID, error)
}
type ReplyRouteStore interface {
	ResolveReply(context.Context, int64) (domain.SessionID, bool, error)
}
type Preferences = settingsport.Preferences
type PreferenceSnapshot = settingsport.Snapshot
type ProviderPreference = settingsport.ProviderPreference
type ProviderPreferences = settingsport.ProviderPreferences
type CreateDraft struct {
	ComputerID domain.ComputerID
	Provider   domain.Provider
	Workdir    string
	Confirmed  bool
}
type CreateDraftSelector interface {
	PreviewCreateDraft(context.Context, domain.Provider) (CreateDraft, error)
	ConfirmCreateDraft(context.Context, domain.Provider, int64) (CreateDraft, error)
}
type Lifecycle interface {
	Abort(context.Context, app.StartSessionRequest, domain.ProviderBinding) error
}
type ArchivedResumer interface {
	Resume(context.Context, domain.SessionID) (domain.Session, error)
}
type AsyncArchivedResumer interface {
	BeginResume(context.Context, domain.SessionID) (PendingSessionStart, error)
}
type SessionCloser interface {
	Close(context.Context, domain.SessionID) (app.CloseSessionResult, error)
}
type TurnLifecycle interface {
	Start(context.Context, domain.SessionID) (domain.Session, error)
	BeginStop(context.Context, domain.SessionID) (domain.Session, error)
	Finish(context.Context, domain.SessionID) (domain.Session, bool, error)
}
type NotificationKind string

const (
	NotificationCommentary NotificationKind = "commentary"
	NotificationQuestion   NotificationKind = "question"
	NotificationFinal      NotificationKind = "final"
	NotificationError      NotificationKind = "error"
)

type Notification struct {
	OperationID    string
	ConversationID int64
	SessionID      domain.SessionID
	Kind           NotificationKind
	Text           string
}
type OutgoingNotification struct {
	OperationID    string
	ConversationID int64
	SessionID      domain.SessionID
	Kind           NotificationKind
	Payload        []byte
}
type OutputReceipt struct {
	Inserted    bool
	SessionID   domain.SessionID
	OperationID string
	Sequence    uint64
}
type DurableOutputCustody interface {
	AcceptOutput(context.Context, OutgoingNotification) (OutputReceipt, error)
}
type AuthorizationStart struct {
	OperationID      string
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
	ComputerID       domain.ComputerID
	Provider         domain.Provider
}
type AuthorizationChallenge struct {
	OperationID        string
	ComputerID         domain.ComputerID
	Provider           domain.Provider
	ChallengeReference string
	Instruction        string
}
type AuthorizationSecret struct {
	OperationID           string
	SubmissionOperationID string
	ActorID               int64
	PrivateChatID         int64
	ConversationKind      string
	SourceMessageID       int64
	ComputerID            domain.ComputerID
	Provider              domain.Provider
	ChallengeReference    string
	Secret                []byte
}
type AuthorizationResult struct {
	Authenticated bool
	DeletionKnown bool
}
type AuthorizationPendingLookup struct {
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
}
type PendingAuthorization struct {
	AuthorizationChallenge
	AcceptsSecret bool
}
type AuthorizationDiscard struct {
	OperationID      string
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
	SourceMessageID  int64
}
type AuthorizationMessageLookup struct {
	ActorID          int64
	PrivateChatID    int64
	ConversationKind string
	SourceMessageID  int64
}
type AuthorizationMessageBinding struct {
	Bound         bool
	Provider      domain.Provider
	Authenticated bool
	DeletionKnown bool
}
type AuthorizationFlow interface {
	SupportsAuthorization(domain.Provider) bool
	StartAuthorization(context.Context, AuthorizationStart) (AuthorizationChallenge, error)
	ConsumeAuthorizationMessage(context.Context, AuthorizationMessageLookup) (AuthorizationMessageBinding, error)
	PendingAuthorizations(context.Context, AuthorizationPendingLookup) ([]PendingAuthorization, error)
	SubmitAuthorization(context.Context, AuthorizationSecret) (AuthorizationResult, error)
	DiscardAuthorizationMessage(context.Context, AuthorizationDiscard) (AuthorizationResult, error)
}
type SemanticActionKind string

const (
	SemanticPagePrevious                SemanticActionKind = "page_previous"
	SemanticPageLatest                  SemanticActionKind = "page_latest"
	SemanticPageNext                    SemanticActionKind = "page_next"
	SemanticStop                        SemanticActionKind = "stop"
	SemanticClose                       SemanticActionKind = "close"
	SemanticOptions                     SemanticActionKind = "options"
	SemanticScreen                      SemanticActionKind = "screen"
	SemanticSelect                      SemanticActionKind = "select_session"
	SemanticResume                      SemanticActionKind = "resume"
	SemanticMenuSessions                SemanticActionKind = "menu_sessions"
	SemanticMenuNew                     SemanticActionKind = "menu_new"
	SemanticMenuArchive                 SemanticActionKind = "menu_archive"
	SemanticMenuStatus                  SemanticActionKind = "menu_status"
	SemanticMenuSettings                SemanticActionKind = "menu_settings"
	SemanticMenuBack                    SemanticActionKind = "menu_back"
	SemanticCreateCodex                 SemanticActionKind = "create_codex"
	SemanticCreateClaude                SemanticActionKind = "create_claude"
	SemanticSettingsScreen              SemanticActionKind = "settings_screen"
	SemanticSettingsDetail              SemanticActionKind = "settings_detail"
	SemanticSettingsContinueExisting    SemanticActionKind = "settings_continue_existing"
	SemanticSettingsTechnicalActions    SemanticActionKind = "settings_technical_actions"
	SemanticSettingsBackgroundQuestions SemanticActionKind = "settings_background_questions"
	SemanticSettingsBackgroundErrors    SemanticActionKind = "settings_background_errors"
	SemanticSettingsLifetimeNever       SemanticActionKind = "settings_lifetime_never"
	SemanticSettingsLifetime6Hours      SemanticActionKind = "settings_lifetime_6h"
	SemanticSettingsLifetime12Hours     SemanticActionKind = "settings_lifetime_12h"
	SemanticSettingsLifetime24Hours     SemanticActionKind = "settings_lifetime_24h"
	SemanticSettingsLifetime48Hours     SemanticActionKind = "settings_lifetime_48h"
	SemanticSettingsProviderCodex       SemanticActionKind = "settings_provider_codex"
	SemanticSettingsProviderClaude      SemanticActionKind = "settings_provider_claude"
	SemanticAuthorizeCodex              SemanticActionKind = "authorize_codex"
	SemanticAuthorizeClaude             SemanticActionKind = "authorize_claude"
)

type SemanticAction struct {
	Kind         SemanticActionKind
	SessionID    domain.SessionID
	Page         int
	FollowLatest bool
	SessionSlot  int
	UpdateID     int64
}
type SemanticCarrierEffect string

const SemanticEditSameCarrier SemanticCarrierEffect = "edit_same_carrier"

type SemanticContentPage struct {
	Content string
	Anchors []string
}
type SemanticPageView struct {
	Page         int
	Pages        int
	Anchor       string
	FollowLatest bool
}
type SemanticCard struct {
	SessionID            domain.SessionID
	Effect               SemanticCarrierEffect
	Header               string
	Pages                []SemanticContentPage
	View                 SemanticPageView
	Working              bool
	Archived             bool
	OptionsExpanded      bool
	SelectableSessionIDs []domain.SessionID
	SessionRowSizes      []int
	MakeActive           bool
}
type SemanticActionResult struct {
	// Decision keeps the unsigned legacy message path operational. Signed
	// production composition must consume Card instead of parsing Decision.
	Decision coordinator.Decision
	Card     *SemanticCard
	Surface  *SemanticSurface
}
type SemanticButton struct {
	Label     string
	Action    SemanticActionKind
	SessionID domain.SessionID
}
type SemanticSurface struct {
	Text string
	Rows [][]SemanticButton
}

// ProjectCurrent returns a read-only current projection for an exact session,
// or for the global active surface when sessionID is empty.
func (controller *Controller) ProjectCurrent(ctx context.Context, sessionID domain.SessionID) (SemanticActionResult, error) {
	if sessionID == "" {
		controller.mu.Lock()
		sessionID = controller.active
		controller.mu.Unlock()
		if sessionID == "" {
			if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
				sessionID, _ = loader.LoadActiveSession(ctx)
			}
		}
	}
	if sessionID == "" {
		return SemanticActionResult{Surface: mainMenuSurface("Bria готова. Нет активной сессии.")}, nil
	}
	card, err := controller.semanticCard(ctx, sessionID, false)
	return SemanticActionResult{Card: &card}, err
}

// HandleSemanticMessage is the unsigned-message ingress for signed UI
// composition. It executes the message once and returns only neutral typed UI
// data; callers must not forward Decision.Keyboard to Telegram.
func (controller *Controller) HandleSemanticMessage(ctx context.Context, update coordinator.Update) (SemanticActionResult, error) {
	if update.Kind != coordinator.UpdateMessage {
		return SemanticActionResult{}, errors.New("semantic message handler requires a message update")
	}
	decision, err := controller.Handle(ctx, update)
	if err != nil {
		return SemanticActionResult{}, err
	}
	result := SemanticActionResult{Decision: decision}
	if decision.Kind != coordinator.DecisionStatus {
		return result, nil
	}
	text := strings.TrimSpace(update.Text)
	switch text {
	case "/menu":
		result.Surface = mainMenuSurface(decision.Status.Text)
		return result, nil
	case "/sessions":
		return controller.sessionListSemanticResult(ctx)
	case "/status":
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active == "" {
			result.Surface = mainMenuSurface(decision.Status.Text)
			return result, nil
		}
		card, cardErr := controller.semanticCard(ctx, active, false)
		result.Card = &card
		return result, cardErr
	}
	if _, _, ok := parseNew(text); ok {
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active != "" {
			card, cardErr := controller.semanticCard(ctx, active, true)
			result.Card = &card
			return result, cardErr
		}
	}
	if sessionID, ok := parseUse(text); ok {
		if _, loadErr := controller.sessions.Load(ctx, sessionID); loadErr == nil {
			card, cardErr := controller.semanticCard(ctx, sessionID, true)
			result.Card = &card
			return result, cardErr
		}
	}
	result.Surface = &SemanticSurface{Text: decision.Status.Text}
	return result, nil
}

// HandleSemanticAction applies a callback action only after an outer signed,
// one-time callback boundary has authenticated it.
func (controller *Controller) HandleSemanticAction(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	if err := validateSemanticAction(action); err != nil {
		return SemanticActionResult{}, err
	}
	if isGlobalSemanticAction(action.Kind) {
		return controller.handleGlobalSemanticAction(ctx, action)
	}
	var decision coordinator.Decision
	var err error
	makeActive := false
	switch action.Kind {
	case SemanticPagePrevious:
		decision, err = controller.cardDecision(ctx, action.SessionID, "pg:prev")
	case SemanticPageLatest:
		decision, err = controller.cardDecision(ctx, action.SessionID, "pg:jump")
	case SemanticPageNext:
		decision, err = controller.cardDecision(ctx, action.SessionID, "pg:next")
	case SemanticStop:
		decision = controller.stopSession(ctx, action.SessionID)
	case SemanticClose:
		decision, err = controller.CloseSession(ctx, action.SessionID)
	case SemanticOptions:
		controller.mu.Lock()
		controller.optionsExpanded[action.SessionID] = !controller.optionsExpanded[action.SessionID]
		controller.mu.Unlock()
		decision, err = controller.cardDecision(ctx, action.SessionID, "")
	case SemanticScreen:
		if controller.settings == nil {
			return SemanticActionResult{}, errors.New("screen settings are not configured")
		}
		if err := controller.settings.ToggleScreen(ctx); err != nil {
			return SemanticActionResult{}, fmt.Errorf("toggle global screen setting: %w", err)
		}
		decision, err = controller.cardDecision(ctx, action.SessionID, "")
	case SemanticSelect:
		decision, err = controller.use(ctx, action.SessionID)
		makeActive = true
	case SemanticResume:
		decision, err = controller.ResumeArchived(ctx, action.SessionID)
		makeActive = true
	default:
		return SemanticActionResult{}, fmt.Errorf("unsupported semantic action %q", action.Kind)
	}
	if err != nil {
		return SemanticActionResult{}, err
	}
	card, err := controller.semanticCard(ctx, action.SessionID, makeActive)
	if err != nil {
		return SemanticActionResult{}, err
	}
	return SemanticActionResult{Decision: decision, Card: &card}, nil
}
func isGlobalSemanticAction(kind SemanticActionKind) bool {
	switch kind {
	case SemanticMenuSessions, SemanticMenuNew, SemanticMenuArchive, SemanticMenuStatus,
		SemanticMenuSettings, SemanticMenuBack, SemanticCreateCodex, SemanticCreateClaude,
		SemanticSettingsScreen, SemanticSettingsDetail, SemanticSettingsContinueExisting,
		SemanticSettingsTechnicalActions, SemanticSettingsBackgroundQuestions, SemanticSettingsBackgroundErrors,
		SemanticSettingsLifetimeNever, SemanticSettingsLifetime6Hours, SemanticSettingsLifetime12Hours,
		SemanticSettingsLifetime24Hours, SemanticSettingsLifetime48Hours,
		SemanticSettingsProviderCodex, SemanticSettingsProviderClaude,
		SemanticAuthorizeCodex, SemanticAuthorizeClaude:
		return true
	}
	return false
}
func (controller *Controller) handleGlobalSemanticAction(ctx context.Context, action SemanticAction) (SemanticActionResult, error) {
	switch action.Kind {
	case SemanticMenuSessions:
		return controller.sessionListSemanticResult(ctx)
	case SemanticMenuNew:
		surface, err := controller.newSessionSurface(ctx)
		return SemanticActionResult{Surface: surface}, err
	case SemanticMenuArchive:
		return controller.archiveSemanticResult(ctx)
	case SemanticMenuStatus:
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active == "" {
			return SemanticActionResult{Surface: mainMenuSurface("Bria готова. Нет активной сессии.")}, nil
		}
		decision, err := controller.cardDecision(ctx, active, "")
		if err != nil {
			return SemanticActionResult{}, err
		}
		card, err := controller.semanticCard(ctx, active, false)
		return SemanticActionResult{Decision: decision, Card: &card}, err
	case SemanticMenuSettings:
		return controller.settingsSemanticResult(ctx)
	case SemanticMenuBack:
		return SemanticActionResult{Surface: mainMenuSurface("Меню")}, nil
	case SemanticCreateCodex, SemanticCreateClaude:
		provider := domain.ProviderCodex
		if action.Kind == SemanticCreateClaude {
			provider = domain.ProviderClaude
		}
		enabled, err := controller.providerEnabled(ctx, provider)
		if err != nil {
			return SemanticActionResult{}, err
		}
		if !enabled {
			return SemanticActionResult{Surface: unavailableNewSessionSurface()}, nil
		}
		if controller.createDrafts == nil {
			return SemanticActionResult{}, errors.New("confirmed session draft selector is not configured")
		}
		draft, err := controller.createDrafts.ConfirmCreateDraft(ctx, provider, action.UpdateID)
		if err != nil {
			return SemanticActionResult{}, fmt.Errorf("confirm session creation draft: %w", err)
		}
		if err := validateCreateDraft(draft, provider, true); err != nil {
			return SemanticActionResult{}, err
		}
		decision, err := controller.create(ctx, action.UpdateID, draft.ComputerID, provider, draft.Workdir)
		if errors.Is(err, errProviderUnavailable) {
			return SemanticActionResult{Surface: unavailableNewSessionSurface()}, nil
		}
		if err != nil {
			return SemanticActionResult{}, err
		}
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		card, err := controller.semanticCard(ctx, active, true)
		return SemanticActionResult{Decision: decision, Card: &card}, err
	case SemanticSettingsScreen, SemanticSettingsDetail, SemanticSettingsContinueExisting, SemanticSettingsTechnicalActions,
		SemanticSettingsBackgroundQuestions, SemanticSettingsBackgroundErrors,
		SemanticSettingsLifetimeNever, SemanticSettingsLifetime6Hours, SemanticSettingsLifetime12Hours,
		SemanticSettingsLifetime24Hours, SemanticSettingsLifetime48Hours, SemanticSettingsProviderCodex, SemanticSettingsProviderClaude:
		if err := telegramsettings.Apply(ctx, controller.settings, controller.providerPreferences, string(action.Kind)); err != nil {
			return SemanticActionResult{}, err
		}
		return controller.settingsSemanticResult(ctx)
	case SemanticAuthorizeCodex, SemanticAuthorizeClaude:
		provider := domain.ProviderCodex
		if action.Kind == SemanticAuthorizeClaude {
			provider = domain.ProviderClaude
		}
		return controller.startAuthorization(ctx, action.UpdateID, provider)
	default:
		return SemanticActionResult{}, fmt.Errorf("unsupported global semantic action %q", action.Kind)
	}
}
func mainMenuSurface(text string) *SemanticSurface {
	return &SemanticSurface{Text: text, Rows: [][]SemanticButton{
		{{Label: "Сессии", Action: SemanticMenuSessions}, {Label: "Новая", Action: SemanticMenuNew}},
		{{Label: "Архив", Action: SemanticMenuArchive}, {Label: "Статус", Action: SemanticMenuStatus}},
		{{Label: "Настройки", Action: SemanticMenuSettings}},
	}}
}
func (controller *Controller) newSessionSurface(ctx context.Context) (*SemanticSurface, error) {
	if controller.createDrafts == nil {
		return &SemanticSurface{Text: "Создание сессии через Telegram не настроено.", Rows: [][]SemanticButton{{{Label: "Меню", Action: SemanticMenuBack}}}}, nil
	}
	providers, err := controller.availableProviders(ctx)
	if err != nil {
		return nil, err
	}
	if len(providers) == 0 {
		return unavailableNewSessionSurface(), nil
	}
	lines := []string{"Новая сессия", "Проверьте выбранный компьютер и рабочую папку перед подтверждением:"}
	buttons := make([]SemanticButton, 0, len(providers))
	for _, provider := range providers {
		draft, err := controller.createDrafts.PreviewCreateDraft(ctx, provider)
		if err != nil {
			return nil, fmt.Errorf("load session creation draft: %w", err)
		}
		if err := validateCreateDraft(draft, provider, false); err != nil {
			return nil, err
		}
		lines = append(lines, fmt.Sprintf("%s: компьютер %s, папка %s", authorizationProviderName(provider), draft.ComputerID, draft.Workdir))
		action := SemanticCreateCodex
		if provider == domain.ProviderClaude {
			action = SemanticCreateClaude
		}
		buttons = append(buttons, SemanticButton{Label: authorizationProviderName(provider), Action: action})
	}
	return &SemanticSurface{Text: strings.Join(lines, "\n"), Rows: [][]SemanticButton{buttons, {{Label: "Меню", Action: SemanticMenuBack}}}}, nil
}
func (controller *Controller) availableProviders(ctx context.Context) ([]domain.Provider, error) {
	known := []domain.Provider{domain.ProviderCodex, domain.ProviderClaude}
	if controller.providerPreferences == nil {
		return known, nil
	}
	preferences, err := controller.providerPreferences.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	available := make(map[domain.Provider]bool, len(preferences))
	for _, preference := range preferences {
		available[preference.Provider] = preference.Configured && preference.Enabled
	}
	result := make([]domain.Provider, 0, len(known))
	for _, provider := range known {
		if available[provider] {
			result = append(result, provider)
		}
	}
	return result, nil
}
func (controller *Controller) providerEnabled(ctx context.Context, provider domain.Provider) (bool, error) {
	providers, err := controller.availableProviders(ctx)
	if err != nil {
		return false, err
	}
	for _, available := range providers {
		if available == provider {
			return true, nil
		}
	}
	return false, nil
}
func unavailableNewSessionSurface() *SemanticSurface {
	return &SemanticSurface{Text: "Создание новой сессии недоступно: нет настроенного и включенного исполнителя.", Rows: [][]SemanticButton{{{Label: "Меню", Action: SemanticMenuBack}}}}
}
func validateCreateDraft(draft CreateDraft, provider domain.Provider, requireConfirmed bool) error {
	if draft.ComputerID == "" || draft.Provider != provider || strings.TrimSpace(draft.Workdir) != draft.Workdir ||
		!filepath.IsAbs(draft.Workdir) || (requireConfirmed && !draft.Confirmed) {
		return errors.New("session creation draft is incomplete or not explicitly confirmed")
	}
	return nil
}
func (controller *Controller) sessionListSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	var text strings.Builder
	text.WriteString("Сессии")
	rows := make([][]SemanticButton, 0, (len(sessions)+2)/3+2)
	row := make([]SemanticButton, 0, 3)
	for _, session := range sessions {
		fmt.Fprintf(&text, "\n%s %s %s", session.Provider(), shortID(session.ID()), session.Status())
		row = append(row, SemanticButton{Label: string(session.Provider()) + " " + shortID(session.ID()), Action: SemanticSelect, SessionID: session.ID()})
		if len(row) == 3 {
			rows = append(rows, row)
			row = make([]SemanticButton, 0, 3)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []SemanticButton{{Label: "Новая", Action: SemanticMenuNew}, {Label: "Меню", Action: SemanticMenuBack}})
	return SemanticActionResult{Surface: &SemanticSurface{Text: text.String(), Rows: rows}}, nil
}
func (controller *Controller) archiveSemanticResult(ctx context.Context) (SemanticActionResult, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return SemanticActionResult{}, err
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	var text strings.Builder
	text.WriteString("Архив")
	rows := make([][]SemanticButton, 0)
	for _, session := range sessions {
		if session.Status() != domain.SessionArchived {
			continue
		}
		fmt.Fprintf(&text, "\n%s %s %s", session.Provider(), shortID(session.ID()), session.Workdir())
		rows = append(rows, []SemanticButton{{Label: "Продолжить " + shortID(session.ID()), Action: SemanticResume, SessionID: session.ID()}})
	}
	rows = append(rows, []SemanticButton{{Label: "Меню", Action: SemanticMenuBack}})
	return SemanticActionResult{Surface: &SemanticSurface{Text: text.String(), Rows: rows}}, nil
}
func (controller *Controller) startAuthorization(ctx context.Context, updateID int64, provider domain.Provider) (SemanticActionResult, error) {
	if controller.authorization == nil {
		return SemanticActionResult{Surface: &SemanticSurface{Text: "Авторизация через Telegram не настроена."}}, nil
	}
	if !controller.authorization.SupportsAuthorization(provider) {
		return SemanticActionResult{Surface: &SemanticSurface{
			Text: "Авторизация " + string(provider) + " через Telegram недоступна на этом компьютере.",
			Rows: [][]SemanticButton{{{Label: "Назад", Action: SemanticMenuSettings}}},
		}}, nil
	}
	operationID := "telegram-update:" + strconv.FormatInt(updateID, 10) + ":authorization"
	challenge, err := controller.authorization.StartAuthorization(ctx, AuthorizationStart{
		OperationID: operationID, ActorID: controller.ownerUserID,
		PrivateChatID: controller.ownerPrivateChatID, ConversationKind: "private",
		ComputerID: controller.localComputerID, Provider: provider,
	})
	if err != nil {
		return SemanticActionResult{Surface: &SemanticSurface{Text: "Не удалось начать авторизацию. Секретные данные не принимались."}}, nil
	}
	if challenge.OperationID != operationID || challenge.ComputerID != controller.localComputerID ||
		challenge.Provider != provider || strings.TrimSpace(challenge.ChallengeReference) == "" ||
		strings.TrimSpace(challenge.Instruction) == "" {
		return SemanticActionResult{}, errors.New("authorization flow returned an inconsistent safe challenge")
	}
	challenge.Instruction = strings.TrimSpace(challenge.Instruction)
	controller.mu.Lock()
	copyChallenge := challenge
	controller.pendingAuthorization = &copyChallenge
	controller.mu.Unlock()
	return SemanticActionResult{Surface: &SemanticSurface{Text: challenge.Instruction}}, nil
}
func (controller *Controller) submitAuthorization(ctx context.Context, update coordinator.Update, challenge AuthorizationChallenge) (coordinator.Decision, error) {
	if update.SourceMessageID <= 0 {
		return coordinator.Decision{}, errors.New("authorization secret message cannot be addressed")
	}
	secret := []byte(update.Text)
	defer func() {
		for index := range secret {
			secret[index] = 0
		}
	}()
	result, err := controller.authorization.SubmitAuthorization(ctx, AuthorizationSecret{
		OperationID: challenge.OperationID, SubmissionOperationID: "telegram-message:" + strconv.FormatInt(update.SourceMessageID, 10) + ":authorization",
		ActorID: controller.ownerUserID, PrivateChatID: controller.ownerPrivateChatID,
		ConversationKind: "private", SourceMessageID: update.SourceMessageID,
		ComputerID: challenge.ComputerID, Provider: challenge.Provider,
		ChallengeReference: challenge.ChallengeReference, Secret: secret,
	})
	if err != nil || !result.Authenticated {
		if !result.DeletionKnown {
			return coordinator.Decision{}, errors.New("authorization secret deletion remains unconfirmed")
		}
		return controller.status("Авторизация не подтверждена. Сообщение с секретом удалено."), nil
	}
	controller.mu.Lock()
	if controller.pendingAuthorization != nil && controller.pendingAuthorization.OperationID == challenge.OperationID {
		controller.pendingAuthorization = nil
	}
	controller.mu.Unlock()
	return controller.status(authorizationProviderName(challenge.Provider) + " авторизован. Сообщение с секретом удалено."), nil
}
func authorizationProviderName(provider domain.Provider) string {
	switch provider {
	case domain.ProviderCodex:
		return "Codex"
	case domain.ProviderClaude:
		return "Claude"
	default:
		return "Провайдер"
	}
}
func (controller *Controller) consumeAuthorizationMessage(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool, error) {
	if controller.authorization == nil || update.SourceMessageID <= 0 {
		return coordinator.Decision{}, false, nil
	}
	binding, err := controller.authorization.ConsumeAuthorizationMessage(ctx, AuthorizationMessageLookup{
		ActorID: update.ActorID, PrivateChatID: update.ConversationID,
		ConversationKind: update.ConversationKind, SourceMessageID: update.SourceMessageID,
	})
	if err != nil {
		return coordinator.Decision{}, true, errors.New("authorization message tombstone is unavailable")
	}
	if !binding.Bound {
		return coordinator.Decision{}, false, nil
	}
	if !binding.DeletionKnown {
		return coordinator.Decision{}, true, errors.New("authorization message deletion remains unconfirmed")
	}
	if binding.Authenticated {
		if binding.Provider != domain.ProviderCodex && binding.Provider != domain.ProviderClaude {
			return coordinator.Decision{}, true, errors.New("authorization tombstone has invalid provider")
		}
		return controller.status(authorizationProviderName(binding.Provider) + " авторизован. Сообщение с секретом удалено."), true, nil
	}
	return controller.status("Авторизация не подтверждена. Сообщение с секретом удалено."), true, nil
}
func interactionTextInput(update coordinator.Update) InteractionTextInput {
	return InteractionTextInput{
		ActorID: update.ActorID, ConversationID: update.ConversationID, ConversationKind: update.ConversationKind,
		SourceMessageID: update.SourceMessageID, ReplyToMessageID: update.ReplyToMessageID,
		Text: update.Text, Caption: update.Caption, MediaKind: update.MediaKind,
	}
}
func (controller *Controller) interactionTextDecision(result InteractionTextResult) (coordinator.Decision, error) {
	if result.Secret && !result.DeletionKnown {
		return coordinator.Decision{}, errors.New("pending interaction secret deletion is unconfirmed")
	}
	if strings.TrimSpace(result.Status) == "" {
		return coordinator.Decision{}, errors.New("pending interaction returned no safe status")
	}
	return controller.status(result.Status), nil
}
func (controller *Controller) routeAuthorizationMessage(ctx context.Context, update coordinator.Update) (coordinator.Decision, bool, error) {
	if controller.authorization == nil {
		return coordinator.Decision{}, false, nil
	}
	pending, err := controller.authorization.PendingAuthorizations(ctx, AuthorizationPendingLookup{
		ActorID: controller.ownerUserID, PrivateChatID: controller.ownerPrivateChatID, ConversationKind: "private",
	})
	if err != nil || len(pending) != 1 {
		if err == nil && len(pending) == 0 {
			return coordinator.Decision{}, false, nil
		}
		decision, discardErr := controller.discardAuthorizationMessage(ctx, update, "Нельзя однозначно сопоставить сообщение с авторизацией.")
		return decision, true, discardErr
	}
	operation := pending[0]
	challenge := operation.AuthorizationChallenge
	if update.Text == "" || update.Caption != "" || update.MediaKind != "" {
		decision, discardErr := controller.discardAuthorizationMessage(ctx, update, "Во время авторизации принимается только отдельное текстовое сообщение с секретом.")
		return decision, true, discardErr
	}
	if !operation.AcceptsSecret || challenge.OperationID == "" || challenge.ComputerID != controller.localComputerID ||
		!controller.authorization.SupportsAuthorization(challenge.Provider) || strings.TrimSpace(challenge.ChallengeReference) == "" {
		decision, discardErr := controller.discardAuthorizationMessage(ctx, update, "Авторизация пока не готова принять секрет.")
		return decision, true, discardErr
	}
	controller.mu.Lock()
	copyChallenge := challenge
	controller.pendingAuthorization = &copyChallenge
	controller.mu.Unlock()
	decision, submitErr := controller.submitAuthorization(ctx, update, challenge)
	return decision, true, submitErr
}
func (controller *Controller) discardAuthorizationMessage(ctx context.Context, update coordinator.Update, reason string) (coordinator.Decision, error) {
	if update.SourceMessageID <= 0 {
		return coordinator.Decision{}, errors.New("authorization message deletion cannot be addressed")
	}
	result, err := controller.authorization.DiscardAuthorizationMessage(ctx, AuthorizationDiscard{
		OperationID: "telegram-message:" + strconv.FormatInt(update.SourceMessageID, 10) + ":delete",
		ActorID:     controller.ownerUserID, PrivateChatID: controller.ownerPrivateChatID,
		ConversationKind: "private", SourceMessageID: update.SourceMessageID,
	})
	if err != nil || !result.DeletionKnown {
		return coordinator.Decision{}, errors.New("authorization message deletion remains unconfirmed")
	}
	return controller.status(reason + " Сообщение удалено."), nil
}
func validateSemanticAction(action SemanticAction) error {
	if isGlobalSemanticAction(action.Kind) {
		if action.SessionID != "" || action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("global semantic action must not contain a session or target fields")
		}
		if (action.Kind == SemanticCreateCodex || action.Kind == SemanticCreateClaude || action.Kind == SemanticAuthorizeCodex || action.Kind == SemanticAuthorizeClaude) && action.UpdateID <= 0 {
			return errors.New("side-effecting global action requires the claimed Telegram update id")
		}
		return nil
	}
	if action.SessionID == "" {
		return errors.New("semantic action session id is required")
	}
	switch action.Kind {
	case SemanticPagePrevious, SemanticPageNext:
		if action.Page < 1 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("semantic page action target is invalid")
		}
	case SemanticPageLatest:
		if action.Page != 0 || !action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("semantic latest-page target is invalid")
		}
	case SemanticStop, SemanticClose, SemanticOptions, SemanticScreen, SemanticSelect, SemanticResume:
		if action.Page != 0 || action.FollowLatest || action.SessionSlot != 0 {
			return errors.New("semantic non-page action must not contain target fields")
		}
	default:
		return fmt.Errorf("unsupported semantic action %q", action.Kind)
	}
	return nil
}

type Options struct {
	QueueLimit  int
	Lifecycle   Lifecycle
	UIState     ActiveSessionStore
	Settings    Preferences
	Providers   ProviderPreferences
	ReplyRoutes ReplyRouteStore
	Stopper     sessionruntime.TurnStopper
	// InputPreparer is required for downloadable voice/photo content. Document
	// preparation additionally requires AllowDocumentInput because documents
	// can carry arbitrary bytes.
	InputPreparer      InputPreparer
	AllowDocumentInput bool
	DurableInput       DurableInputCustody
	DurableOutput      DurableOutputCustody
	Interactions       InteractionHandler
	InteractionText    InteractionTextHandler
	Authorization      AuthorizationFlow
	CreateDrafts       CreateDraftSelector
	Attachments        AttachmentCustody
	RuntimeEvents      RuntimeEventObserver
	Finals             FinalProcessor
	AsyncCreator       AsyncSessionCreator
	ArchivedResumer    ArchivedResumer
	AsyncResumer       AsyncArchivedResumer
	SessionCloser      SessionCloser
	TurnLifecycle      TurnLifecycle
	OutputFailures     OutputFailureRecorder
	Recovered          []domain.Session
}
type Controller struct {
	ownerUserID          int64
	ownerPrivateChatID   int64
	localComputerID      domain.ComputerID
	creator              SessionCreator
	sessions             SessionStore
	submitter            sessionruntime.Submitter
	notifier             Notifier
	lifecycle            Lifecycle
	uiState              ActiveSessionStore
	settings             Preferences
	providerPreferences  ProviderPreferences
	replyRoutes          ReplyRouteStore
	stopper              sessionruntime.TurnStopper
	inputPreparer        InputPreparer
	allowDocumentInput   bool
	durableInput         DurableInputCustody
	durableOutput        DurableOutputCustody
	interactions         InteractionHandler
	interactionText      InteractionTextHandler
	authorization        AuthorizationFlow
	createDrafts         CreateDraftSelector
	attachments          AttachmentCustody
	runtimeEvents        RuntimeEventObserver
	finals               FinalProcessor
	asyncCreator         AsyncSessionCreator
	archivedResumer      ArchivedResumer
	asyncResumer         AsyncArchivedResumer
	sessionCloser        SessionCloser
	turnLifecycle        TurnLifecycle
	outputFailures       OutputFailureRecorder
	queueLimit           int
	rootContext          context.Context
	cancelRoot           context.CancelFunc
	mu                   sync.Mutex
	closed               bool
	closeDone            chan struct{}
	closeErr             error
	active               domain.SessionID
	live                 map[domain.SessionID]domain.Session
	pending              map[domain.SessionID]domain.Session
	workers              map[domain.SessionID]*sessionWorker
	created              map[domain.SessionID]createdProcess
	history              map[domain.SessionID][]string
	page                 map[domain.SessionID]int
	followLatest         map[domain.SessionID]bool
	optionsExpanded      map[domain.SessionID]bool
	deliveryFailures     map[domain.SessionID]NotificationFailure
	pendingAuthorization *AuthorizationChallenge
	creates              sync.WaitGroup
	worker               sync.WaitGroup
}
type createdProcess struct {
	request app.StartSessionRequest
	binding domain.ProviderBinding
}
type queuedTurn struct {
	text        string
	messageID   string
	attachments []AttachmentRef
}
type sessionWorker struct {
	controller   *Controller
	sessionID    domain.SessionID
	queue        chan queuedTurn
	mu           sync.Mutex
	activeCancel context.CancelFunc
	activeTurn   uint64
	stoppingTurn uint64
}

var _ coordinator.Handler = (*Controller)(nil)

func New(
	ownerUserID int64,
	ownerPrivateChatID int64,
	localComputerID domain.ComputerID,
	creator SessionCreator,
	sessions SessionStore,
	submitter sessionruntime.Submitter,
	notifier Notifier,
	options Options,
) (*Controller, error) {
	if ownerUserID <= 0 || ownerPrivateChatID <= 0 ||
		strings.TrimSpace(string(localComputerID)) == "" {
		return nil, errors.New("owner, private chat, and local computer identities are required")
	}
	if (creator == nil && options.AsyncCreator == nil) || sessions == nil || submitter == nil || notifier == nil {
		return nil, errors.New("creator, session store, submitter, and notifier are required")
	}
	if options.QueueLimit < 0 {
		return nil, errors.New("per-session queue limit must not be negative")
	}
	queueLimit := options.QueueLimit
	if queueLimit == 0 {
		queueLimit = defaultQueueLimit
	}
	rootContext, cancelRoot := context.WithCancel(context.Background())
	controller := &Controller{
		ownerUserID: ownerUserID, ownerPrivateChatID: ownerPrivateChatID,
		localComputerID: localComputerID, creator: creator, sessions: sessions,
		submitter: submitter, notifier: notifier, lifecycle: options.Lifecycle,
		queueLimit: queueLimit, rootContext: rootContext, cancelRoot: cancelRoot,
		uiState:             options.UIState,
		settings:            options.Settings,
		providerPreferences: options.Providers,
		replyRoutes:         options.ReplyRoutes,
		stopper:             options.Stopper,
		inputPreparer:       options.InputPreparer,
		allowDocumentInput:  options.AllowDocumentInput,
		durableInput:        options.DurableInput,
		durableOutput:       options.DurableOutput,
		interactions:        options.Interactions,
		interactionText:     options.InteractionText,
		authorization:       options.Authorization,
		asyncCreator:        options.AsyncCreator,
		archivedResumer:     options.ArchivedResumer,
		asyncResumer:        options.AsyncResumer,
		sessionCloser:       options.SessionCloser,
		turnLifecycle:       options.TurnLifecycle,
		createDrafts:        options.CreateDrafts,
		attachments:         options.Attachments,
		runtimeEvents:       options.RuntimeEvents,
		finals:              options.Finals,
		outputFailures:      options.OutputFailures,
		closeDone:           make(chan struct{}),
		live:                make(map[domain.SessionID]domain.Session),
		pending:             make(map[domain.SessionID]domain.Session),
		workers:             make(map[domain.SessionID]*sessionWorker),
		created:             make(map[domain.SessionID]createdProcess),
		history:             make(map[domain.SessionID][]string),
		page:                make(map[domain.SessionID]int),
		followLatest:        make(map[domain.SessionID]bool),
		optionsExpanded:     make(map[domain.SessionID]bool),
		deliveryFailures:    make(map[domain.SessionID]NotificationFailure),
	}
	for _, session := range options.Recovered {
		if session.Status() == domain.SessionReady {
			controller.live[session.ID()] = session
			controller.ensureWorkerLocked(session.ID())
		}
	}
	if loader, ok := options.UIState.(ActiveSessionLoader); ok {
		if active, err := loader.LoadActiveSession(context.Background()); err == nil {
			if _, ok := controller.live[active]; ok {
				controller.active = active
			}
		}
	}
	return controller, nil
}
func (controller *Controller) Handle(
	ctx context.Context,
	update coordinator.Update,
) (coordinator.Decision, error) {
	if update.ActorID != controller.ownerUserID ||
		update.ConversationID != controller.ownerPrivateChatID ||
		update.ConversationKind != "private" {
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
	if update.Kind == coordinator.UpdateCallback {
		return controller.handleCallback(ctx, update)
	}
	if update.Kind != coordinator.UpdateMessage {
		return coordinator.Decision{
			Kind:        coordinator.DecisionBlock,
			BlockReason: "authorized non-text update is not handled by the text controller",
		}, nil
	}
	if err := ctx.Err(); err != nil {
		return coordinator.Decision{}, err
	}
	controller.mu.Lock()
	closed := controller.closed
	controller.mu.Unlock()
	if closed {
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	if decision, handled, err := controller.consumeAuthorizationMessage(ctx, update); handled {
		return decision, err
	}
	if tombstones, ok := controller.interactionText.(InteractionSourceTombstone); ok {
		result, err := tombstones.ConsumeBoundSourceMessage(ctx, interactionTextInput(update))
		if err != nil {
			return coordinator.Decision{}, fmt.Errorf("consume bound interaction message: %w", err)
		}
		if result.Handled {
			return controller.interactionTextDecision(result)
		}
	}
	if decision, handled, err := controller.routeAuthorizationMessage(ctx, update); handled {
		return decision, err
	}
	if controller.interactionText != nil {
		result, err := controller.interactionText.ResolvePendingText(ctx, interactionTextInput(update))
		if err != nil {
			return coordinator.Decision{}, fmt.Errorf("resolve pending interaction text: %w", err)
		}
		if result.Handled {
			return controller.interactionTextDecision(result)
		}
	}
	text := strings.TrimSpace(update.Text)
	switch text {
	case "/menu":
		return controller.menuStatus("Меню"), nil
	case "/status":
		return controller.mainSurface(ctx)
	case "/sessions":
		controller.mu.Lock()
		hasActive := controller.active != ""
		controller.mu.Unlock()
		if hasActive {
			return controller.mainSurface(ctx)
		}
		return controller.listSessions(ctx)
	case "/stop":
		return controller.stopCurrent(ctx), nil
	}
	if provider, workdir, ok := parseNew(text); ok {
		decision, err := controller.create(ctx, update.ID, controller.localComputerID, provider, workdir)
		if errors.Is(err, errProviderUnavailable) {
			return controller.status(unavailableNewSessionSurface().Text), nil
		}
		return decision, err
	}
	if sessionID, ok := parseUse(text); ok {
		return controller.use(ctx, sessionID)
	}
	prepared, rejection := controller.prepareInput(ctx, update)
	if rejection != "" {
		return controller.status(rejection), nil
	}
	if update.ReplyToMessageID > 0 && controller.replyRoutes != nil {
		sessionID, found, err := controller.replyRoutes.ResolveReply(ctx, update.ReplyToMessageID)
		if err != nil {
			return coordinator.Decision{}, fmt.Errorf("resolve replied Telegram message: %w", err)
		}
		if found {
			return controller.enqueueSession(ctx, update.ID, sessionID, prepared), nil
		}
	}
	return controller.enqueue(ctx, update.ID, prepared), nil
}
func (controller *Controller) prepareInput(ctx context.Context, update coordinator.Update) (PreparedInput, string) {
	base := joinPromptParts(update.Text, update.Caption)
	if update.MediaKind == "" {
		if base == "" {
			return PreparedInput{}, "В сообщении нет текста или поддерживаемого вложения."
		}
		return PreparedInput{Text: base}, ""
	}
	input := IncomingInput{
		Kind: update.MediaKind, FileID: update.MediaFileID,
		FileUniqueID: update.MediaFileUniqueID, FileSize: update.MediaFileSize,
		MIMEType: update.MediaMIMEType, DurationSeconds: update.MediaDurationSeconds,
		Width: update.MediaWidth, Height: update.MediaHeight,
		DownloadPermitted: update.MediaDownloadAllowed,
	}
	switch input.Kind {
	case "video":
		metadata := "Видео без загрузки"
		if input.DurationSeconds > 0 {
			metadata += fmt.Sprintf(", длительность %d сек.", input.DurationSeconds)
		}
		if input.Width > 0 && input.Height > 0 {
			metadata += fmt.Sprintf(", размер %dx%d", input.Width, input.Height)
		}
		return PreparedInput{Text: joinPromptParts(base, metadata)}, ""
	case "document":
		if !controller.allowDocumentInput {
			return PreparedInput{}, "Обработка документов не разрешена настройками Bria."
		}
	case "voice", "photo":
		if !input.DownloadPermitted {
			return PreparedInput{}, "Загрузка этого вложения не разрешена."
		}
	default:
		return PreparedInput{}, "Этот тип вложения пока не поддерживается."
	}
	if controller.inputPreparer == nil {
		return PreparedInput{}, "Обработчик этого вложения не настроен."
	}
	if structured, ok := controller.inputPreparer.(StructuredInputPreparer); ok {
		prepared, err := structured.PrepareStructured(ctx, input)
		if err != nil || validatePreparedInput(prepared) != nil {
			return PreparedInput{}, "Не удалось безопасно подготовить вложение."
		}
		prepared.Text = joinPromptParts(base, prepared.Text)
		return prepared, ""
	}
	prepared, err := controller.inputPreparer.Prepare(ctx, input)
	if err != nil {
		return PreparedInput{}, "Не удалось безопасно подготовить вложение."
	}
	prepared = strings.TrimSpace(prepared)
	if prepared == "" {
		return PreparedInput{}, "Вложение не содержит данных для запроса."
	}
	return PreparedInput{Text: joinPromptParts(base, prepared)}, ""
}
func validatePreparedInput(input PreparedInput) error {
	if strings.TrimSpace(input.Text) == "" && len(input.Attachments) == 0 {
		return errors.New("prepared input is empty")
	}
	for _, attachment := range input.Attachments {
		if strings.TrimSpace(attachment.Reference) == "" || attachment.Reference != strings.TrimSpace(attachment.Reference) ||
			filepath.IsAbs(attachment.Reference) || strings.ContainsAny(attachment.Reference, `/\\`) || attachment.Size <= 0 || len(attachment.SHA256) != 64 {
			return errors.New("prepared attachment reference is invalid")
		}
		for _, character := range attachment.SHA256 {
			if !strings.ContainsRune("0123456789abcdef", character) {
				return errors.New("prepared attachment digest is invalid")
			}
		}
	}
	return nil
}
func joinPromptParts(parts ...string) string {
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || (len(result) > 0 && result[len(result)-1] == part) {
			continue
		}
		result = append(result, part)
	}
	return strings.Join(result, "\n\n")
}
func (controller *Controller) handleCallback(ctx context.Context, update coordinator.Update) (coordinator.Decision, error) {
	if update.CallbackQueryID == "" || update.SourceMessageID <= 0 {
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
	if carrier, ok := controller.uiState.(CardCarrierStore); ok {
		controller.mu.Lock()
		active := controller.active
		controller.mu.Unlock()
		if active == "" {
			if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
				active, _ = loader.LoadActiveSession(ctx)
			}
		}
		if active != "" {
			if err := carrier.SetCardCarrier(ctx, active, update.ConversationID, update.SourceMessageID); err != nil {
				return coordinator.Decision{}, fmt.Errorf("persist Telegram card carrier: %w", err)
			}
		}
	}
	callbackText := strings.TrimSpace(update.Text)
	if strings.HasPrefix(callbackText, "session:select:") || strings.HasPrefix(callbackText, "sw:") {
		id := strings.TrimPrefix(callbackText, "session:select:")
		if id == callbackText {
			id = strings.TrimPrefix(callbackText, "sw:")
		}
		decision, err := controller.use(ctx, domain.SessionID(id))
		return withCallbackID(decision, update), err
	}
	switch callbackText {
	case "new:codex", "new:claude":
		provider := domain.ProviderCodex
		if callbackText == "new:claude" {
			provider = domain.ProviderClaude
		}
		enabled, err := controller.providerEnabled(ctx, provider)
		if err != nil {
			return coordinator.Decision{}, err
		}
		if !enabled {
			return withCallbackID(controller.status(unavailableNewSessionSurface().Text), update), nil
		}
		if controller.createDrafts == nil {
			return withCallbackID(controller.status("Сначала выберите и подтвердите компьютер и рабочую папку."), update), nil
		}
		draft, err := controller.createDrafts.ConfirmCreateDraft(ctx, provider, update.ID)
		if err != nil || validateCreateDraft(draft, provider, true) != nil {
			return withCallbackID(controller.status("Не удалось подтвердить компьютер и рабочую папку."), update), nil
		}
		decision, err := controller.create(ctx, update.ID, draft.ComputerID, provider, draft.Workdir)
		if errors.Is(err, errProviderUnavailable) {
			return withCallbackID(controller.status(unavailableNewSessionSurface().Text), update), nil
		}
		return withCallbackID(decision, update), err
	case "mm:status":
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "mm:list":
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "mm:new":
		return withCallbackID(controller.newMenu(ctx), update), nil
	case "mm:arch":
		return withCallbackID(controller.archiveMenu(ctx), update), nil
	case "mm:set":
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "mm:back", "ft:more":
		return withCallbackID(controller.menuStatus("Меню"), update), nil
	case "ft:stop":
		controller.stopCurrent(ctx)
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "pg:prev", "pg:next", "pg:jump":
		decision, err := controller.cardNavigation(ctx, callbackText)
		return withCallbackID(decision, update), err
	case "menu:status":
		return withCallbackID(controller.menuStatus("Bria работает и готова принимать команды."), update), nil
	case "menu:sessions":
		decision, err := controller.mainSurface(ctx)
		return withCallbackID(decision, update), err
	case "menu:new":
		return withCallbackID(controller.newMenu(ctx), update), nil
	case "menu:archive":
		return withCallbackID(controller.menuStatus("Архив пока пуст."), update), nil
	case "menu:settings":
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "settings:screen":
		if controller.settings != nil {
			if err := controller.settings.ToggleScreen(ctx); err != nil {
				return coordinator.Decision{}, err
			}
		}
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "settings:detail":
		if controller.settings != nil {
			if err := controller.settings.ToggleCardDetail(ctx); err != nil {
				return coordinator.Decision{}, err
			}
		}
		decision, err := controller.settingsStatus(ctx)
		return withCallbackID(decision, update), err
	case "card:prev", "card:next", "card:latest":
		decision, err := controller.cardNavigation(ctx, update.Text)
		return withCallbackID(decision, update), err
	default:
		return coordinator.Decision{Kind: coordinator.DecisionSkip}, nil
	}
}
func (controller *Controller) mainSurface(ctx context.Context) (coordinator.Decision, error) {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	if active == "" {
		if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
			active, _ = loader.LoadActiveSession(ctx)
		}
	}
	if active != "" {
		return controller.cardDecision(ctx, active, "")
	}
	return controller.menuStatus("Bria готова. Нет активной сессии. Выберите «Новая» или создайте сессию командой /new."), nil
}
func (controller *Controller) cardNavigation(ctx context.Context, action string) (coordinator.Decision, error) {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	if active == "" {
		if loader, ok := controller.uiState.(ActiveSessionLoader); ok {
			active, _ = loader.LoadActiveSession(ctx)
		}
	}
	if active == "" {
		return controller.menuStatus("Нет активной сессии."), nil
	}
	return controller.cardDecision(ctx, active, action)
}
func (controller *Controller) cardDecision(ctx context.Context, sessionID domain.SessionID, action string) (coordinator.Decision, error) {
	session, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return controller.status("Сессия " + string(sessionID) + " готова."), nil
	}
	controller.mu.Lock()
	items := append([]string(nil), controller.history[sessionID]...)
	controller.mu.Unlock()
	if len(items) == 0 {
		if historyStore, ok := controller.uiState.(CardHistoryStore); ok {
			items, _ = historyStore.LoadCardHistory(ctx, sessionID)
		}
	}
	const pageBytes = 3200
	pages := []string{}
	if len(items) == 0 {
		pages = []string{"Пока нет сообщений исполнителя."}
	} else {
		current := ""
		for _, item := range items {
			if len(current) > 0 && len(current)+len(item)+1 > pageBytes {
				pages = append(pages, current)
				current = ""
			}
			if current != "" {
				current += "\n"
			}
			current += item
		}
		if current != "" {
			pages = append(pages, current)
		}
	}
	controller.mu.Lock()
	page := controller.page[sessionID]
	followLatest := controller.followLatest[sessionID]
	if page < 1 || page > len(pages) {
		page = len(pages)
		followLatest = true
	} else if followLatest && action == "" {
		page = len(pages)
	}
	if action == "card:prev" || action == "pg:prev" {
		page--
		if page < 1 {
			page = len(pages)
		}
	}
	if action == "card:next" || action == "pg:next" {
		page++
		if page > len(pages) {
			page = 1
		}
	}
	if action == "card:latest" || action == "pg:jump" {
		page = len(pages)
	}
	if action != "" {
		followLatest = page == len(pages)
	}
	controller.page[sessionID] = page
	controller.followLatest[sessionID] = followLatest
	controller.mu.Unlock()
	if pager, ok := controller.uiState.(CardPageStore); ok {
		if err := pager.SetCardPage(ctx, sessionID, page, len(pages), "", page == len(pages)); err != nil {
			return coordinator.Decision{}, err
		}
	}
	stateText := string(session.Status())
	if session.Status() == domain.SessionReady {
		stateText = "готова"
	}
	if session.Status() == domain.SessionAwaitingRecovery {
		stateText = "ожидает восстановления"
	}
	controller.mu.Lock()
	usable := false
	if live, ok := controller.live[sessionID]; ok {
		usable = live.Equal(session)
	}
	controller.mu.Unlock()
	if session.Status() == domain.SessionReady && !usable {
		stateText += " (процесс не запущен)"
	}
	header := fmt.Sprintf("Сессия %s\n%s %s\nРабочая папка: %s\nСтраница %d/%d\n\n", session.ID(), session.Provider(), stateText, session.Workdir(), page, len(pages))
	keyboard := coordinator.KeyboardMarkup{{{Text: "‹", CallbackData: "pg:prev"}, {Text: fmt.Sprintf("%d/%d", page, len(pages)), CallbackData: "pg:jump"}, {Text: "›", CallbackData: "pg:next"}}, {{Text: "Стоп", CallbackData: "ft:stop"}, {Text: "Опции", CallbackData: "ft:more"}}}
	if sessions, e := controller.sessions.List(ctx); e == nil {
		sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
		row := []coordinator.KeyboardButton{}
		for _, candidate := range sessions {
			label := string(candidate.Provider()) + " " + shortID(candidate.ID())
			if candidate.ID() == sessionID {
				label = "✓ " + label
			}
			row = append(row, coordinator.KeyboardButton{Text: label, CallbackData: "sw:" + string(candidate.ID())})
			if len(row) == 3 {
				keyboard = append(keyboard, row)
				row = []coordinator.KeyboardButton{}
			}
		}
		if len(row) > 0 {
			keyboard = append(keyboard, row)
		}
	}
	keyboard = append(keyboard, []coordinator.KeyboardButton{{Text: "+ Новая", CallbackData: "mm:new"}, {Text: "≡ Меню", CallbackData: "ft:more"}})
	return coordinator.Decision{Kind: coordinator.DecisionStatus, Status: coordinator.Status{ConversationID: controller.ownerPrivateChatID, Text: header + pages[page-1]}, Keyboard: &keyboard}, nil
}
func (controller *Controller) semanticCard(ctx context.Context, sessionID domain.SessionID, makeActive bool) (SemanticCard, error) {
	session, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return SemanticCard{}, fmt.Errorf("load semantic card session: %w", err)
	}
	controller.mu.Lock()
	items := append([]string(nil), controller.history[sessionID]...)
	page := controller.page[sessionID]
	followLatest := controller.followLatest[sessionID]
	optionsExpanded := controller.optionsExpanded[sessionID]
	controller.mu.Unlock()
	if len(items) == 0 {
		if historyStore, ok := controller.uiState.(CardHistoryStore); ok {
			items, _ = historyStore.LoadCardHistory(ctx, sessionID)
		}
	}
	pages := paginateSemanticHistory(items)
	if page < 1 || page > len(pages) {
		page = len(pages)
		followLatest = true
	}
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return SemanticCard{}, fmt.Errorf("list semantic card sessions: %w", err)
	}
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	selectable := make([]domain.SessionID, 0, len(sessions))
	for _, candidate := range sessions {
		selectable = append(selectable, candidate.ID())
	}
	rowSizes := make([]int, 0, (len(selectable)+2)/3)
	for remaining := len(selectable); remaining > 0; remaining -= 3 {
		size := remaining
		if size > 3 {
			size = 3
		}
		rowSizes = append(rowSizes, size)
	}
	working := session.Status() == domain.SessionRunning || session.Status() == domain.SessionStopping || session.Status() == domain.SessionClosingAfterWork
	stateText := string(session.Status())
	if session.Status() == domain.SessionReady {
		stateText = "готова"
	} else if session.Status() == domain.SessionAwaitingRecovery {
		stateText = "ожидает восстановления"
	}
	return SemanticCard{
		SessionID: sessionID,
		Effect:    SemanticEditSameCarrier,
		Header:    fmt.Sprintf("Сессия %s\n%s %s\nРабочая папка: %s", session.ID(), session.Provider(), stateText, session.Workdir()),
		Pages:     pages,
		View: SemanticPageView{
			Page: page, Pages: len(pages), Anchor: pages[page-1].Anchors[0], FollowLatest: followLatest,
		},
		Working: working, Archived: session.Status() == domain.SessionArchived,
		OptionsExpanded: optionsExpanded, SelectableSessionIDs: selectable,
		SessionRowSizes: rowSizes, MakeActive: makeActive,
	}, nil
}
func paginateSemanticHistory(items []string) []SemanticContentPage {
	if len(items) == 0 {
		return []SemanticContentPage{{Content: "Пока нет сообщений исполнителя.", Anchors: []string{"empty"}}}
	}
	const pageBytes = 3200
	pages := make([]SemanticContentPage, 0, 1)
	current := SemanticContentPage{}
	for index, item := range items {
		separator := ""
		if current.Content != "" {
			separator = "\n"
		}
		if current.Content != "" && len(current.Content)+len(separator)+len(item) > pageBytes {
			pages = append(pages, current)
			current = SemanticContentPage{}
			separator = ""
		}
		current.Content += separator + item
		current.Anchors = append(current.Anchors, "history:"+strconv.Itoa(index+1))
	}
	if current.Content != "" {
		pages = append(pages, current)
	}
	return pages
}
func (controller *Controller) settingsStatus(ctx context.Context) (coordinator.Decision, error) {
	result, err := controller.settingsSemanticResult(ctx)
	if err != nil {
		return coordinator.Decision{}, err
	}
	decision := controller.menuStatus(result.Surface.Text)
	keyboard := coordinator.KeyboardMarkup{{{Text: "Screen", CallbackData: "settings:screen"}, {Text: "Детализация", CallbackData: "settings:detail"}}}
	keyboard = append(keyboard, (*decision.Keyboard)...)
	decision.Keyboard = &keyboard
	return decision, nil
}
func withCallbackID(decision coordinator.Decision, update coordinator.Update) coordinator.Decision {
	if decision.Kind == coordinator.DecisionStatus {
		decision.Status.CallbackQueryID = update.CallbackQueryID
		decision.Status.SourceMessageID = update.SourceMessageID
	}
	return decision
}
func (controller *Controller) menuStatus(text string) coordinator.Decision {
	decision := controller.status(text)
	keyboard := coordinator.KeyboardMarkup{
		{{Text: "Сессии", CallbackData: "menu:sessions"}, {Text: "Новая", CallbackData: "menu:new"}},
		{{Text: "Архив", CallbackData: "menu:archive"}, {Text: "Статус", CallbackData: "menu:status"}},
		{{Text: "Настройки", CallbackData: "menu:settings"}},
	}
	decision.Keyboard = &keyboard
	return decision
}
func (controller *Controller) newMenu(ctx context.Context) coordinator.Decision {
	providers, err := controller.availableProviders(ctx)
	if err != nil || len(providers) == 0 {
		return controller.menuStatus(unavailableNewSessionSurface().Text)
	}
	decision := controller.menuStatus("Новая сессия\nВыберите исполнителя (рабочая папка по умолчанию: /tmp):")
	buttons := make([]coordinator.KeyboardButton, 0, len(providers))
	for _, provider := range providers {
		buttons = append(buttons, coordinator.KeyboardButton{Text: authorizationProviderName(provider), CallbackData: "new:" + string(provider)})
	}
	keyboard := coordinator.KeyboardMarkup{buttons, {{Text: "≡ Меню", CallbackData: "mm:back"}}}
	decision.Keyboard = &keyboard
	return decision
}
func (controller *Controller) archiveMenu(ctx context.Context) coordinator.Decision {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return controller.status("Не удалось прочитать архив.")
	}
	var b strings.Builder
	b.WriteString("Архив\n")
	sort.Slice(sessions, func(i, j int) bool { return sessions[i].ID() < sessions[j].ID() })
	count := 0
	for _, s := range sessions {
		if s.Status() == domain.SessionArchived {
			fmt.Fprintf(&b, "\n%s %s %s", s.Provider(), shortID(s.ID()), s.Workdir())
			count++
		}
	}
	if count == 0 {
		b.WriteString("\nПусто.")
	}
	return controller.menuStatus(b.String())
}
func (controller *Controller) ResumeArchived(ctx context.Context, sessionID domain.SessionID) (coordinator.Decision, error) {
	if controller.asyncResumer != nil {
		return controller.resumeArchivedAsync(ctx, sessionID)
	}
	if controller.archivedResumer == nil {
		return coordinator.Decision{}, errors.New("archived session resumer is not configured")
	}
	archived, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("load archived session before resume: %w", err)
	}
	prior, ok := archived.Binding()
	if archived.Status() != domain.SessionArchived || !ok {
		return coordinator.Decision{}, errors.New("semantic resume target is not an archived provider session")
	}
	resumed, err := controller.archivedResumer.Resume(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("resume archived session: %w", err)
	}
	binding, ok := resumed.Binding()
	if !ok || resumed.ID() != archived.ID() || resumed.Provider() != archived.Provider() ||
		resumed.ComputerID() != archived.ComputerID() || resumed.Workdir() != archived.Workdir() ||
		resumed.Status() != domain.SessionReady || binding.Provider != prior.Provider ||
		binding.SessionID != prior.SessionID || binding.Generation <= prior.Generation {
		return coordinator.Decision{}, errors.New("archived resumer returned an inconsistent exact continuation")
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.live[resumed.ID()] = resumed
	controller.active = resumed.ID()
	controller.ensureWorkerLocked(resumed.ID())
	priorCopy := prior
	controller.created[resumed.ID()] = createdProcess{
		request: app.StartSessionRequest{
			SessionID: resumed.ID(), ComputerID: resumed.ComputerID(), Provider: resumed.Provider(),
			Workdir: resumed.Workdir(), Mode: app.SessionStartResume, PriorBinding: &priorCopy,
		},
		binding: binding,
	}
	controller.mu.Unlock()
	if err := controller.persistActive(ctx, resumed.ID()); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist resumed active session: %w", err)
	}
	return controller.cardDecision(ctx, resumed.ID(), "")
}

// CloseSession applies an already authenticated semantic close action. Busy
// sessions are durably scheduled by SessionCloser and are finalized by the
// worker after the provider terminal.
func (controller *Controller) CloseSession(ctx context.Context, sessionID domain.SessionID) (coordinator.Decision, error) {
	if controller.sessionCloser == nil {
		return coordinator.Decision{}, errors.New("session closer is not configured")
	}
	controller.mu.Lock()
	worker := controller.workers[sessionID]
	controller.mu.Unlock()
	if worker != nil && worker.hasActiveTurn() && controller.turnLifecycle == nil {
		return controller.status("Нельзя безопасно закрыть выполняющуюся сессию без учёта её состояния."), nil
	}
	result, err := controller.sessionCloser.Close(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("close session: %w", err)
	}
	if result.Session.ID() != sessionID {
		return coordinator.Decision{}, errors.New("session closer returned an inconsistent session")
	}
	if result.Scheduled {
		if result.Session.Status() != domain.SessionClosingAfterWork {
			return coordinator.Decision{}, errors.New("scheduled close did not persist closing-after-work")
		}
		controller.replaceLive(result.Session)
		return controller.status("Сессия будет закрыта после текущего запроса."), nil
	}
	if result.Session.Status() != domain.SessionArchived {
		return coordinator.Decision{}, errors.New("confirmed close did not archive the session")
	}
	controller.applyClosedSession(result.Session)
	return controller.archiveMenu(ctx), nil
}
func (controller *Controller) applyClosedSession(session domain.Session) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	delete(controller.live, session.ID())
	if controller.active == session.ID() {
		controller.active = ""
	}
}
func shortID(id domain.SessionID) string {
	s := string(id)
	if len(s) > 8 {
		return s[:8]
	}
	return s
}
func (controller *Controller) status(text string) coordinator.Decision {
	return coordinator.Decision{
		Kind: coordinator.DecisionStatus,
		Status: coordinator.Status{
			ConversationID: controller.ownerPrivateChatID,
			Text:           text,
		},
	}
}

// DeliveryFailure returns the latest unconfirmed notification delivery for a
// session. The state is observational only and never triggers an automatic
// retry.
func (controller *Controller) DeliveryFailure(sessionID domain.SessionID) (NotificationFailure, bool) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	failure, ok := controller.deliveryFailures[sessionID]
	return failure, ok
}

// notify makes exactly one transport attempt. A failed attempt has unknown
// delivery state: Telegram may have accepted it even though no receipt reached
// Bria, so retrying here could duplicate a user-visible message.
func (controller *Controller) notify(ctx context.Context, notification Notification) {
	if controller.durableOutput != nil {
		if notification.OperationID == "" {
			controller.recordNotificationFailure(ctx, notification, false)
			return
		}
		receipt, err := controller.durableOutput.AcceptOutput(ctx, OutgoingNotification{
			OperationID: notification.OperationID, ConversationID: notification.ConversationID,
			SessionID: notification.SessionID, Kind: notification.Kind, Payload: []byte(notification.Text),
		})
		if err != nil || receipt.SessionID != notification.SessionID ||
			receipt.OperationID != notification.OperationID || receipt.Sequence == 0 {
			controller.recordNotificationFailure(ctx, notification, false)
		}
		return
	}
	if err := controller.notifier.Notify(ctx, notification); err == nil {
		return
	}
	controller.recordNotificationFailure(ctx, notification, true)
}
func (controller *Controller) recordNotificationFailure(ctx context.Context, notification Notification, attempted bool) {
	failure := NotificationFailure{
		SessionID: notification.SessionID,
		Kind:      notification.Kind,
		State:     DeliveryUnknown,
	}
	if attempted && controller.outputFailures != nil {
		failure.DurablyRecorded = controller.outputFailures.RecordNotificationFailure(
			context.WithoutCancel(ctx),
			failure,
		) == nil
	}
	controller.mu.Lock()
	controller.deliveryFailures[notification.SessionID] = failure
	controller.mu.Unlock()
}
func (controller *Controller) createAsync(
	ctx context.Context,
	updateID int64,
	computerID domain.ComputerID,
	provider domain.Provider,
	workdir string,
) (coordinator.Decision, error) {
	intent := app.ConfirmedSessionIntent{
		IntentID:   domain.IntentID("telegram-update:" + strconv.FormatInt(updateID, 10)),
		ComputerID: computerID,
		Provider:   provider,
		Workdir:    workdir,
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.creates.Add(1)
	controller.mu.Unlock()
	beginContext, cancelBegin := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(controller.rootContext, cancelBegin)
	pending, err := controller.asyncCreator.BeginCreate(beginContext, intent)
	stopCancellation()
	cancelBegin()
	if err != nil {
		controller.creates.Done()
		return coordinator.Decision{}, fmt.Errorf("begin confirmed session creation: %w", err)
	}
	if err := validatePendingCreate(pending, intent); err != nil {
		controller.creates.Done()
		return coordinator.Decision{}, err
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		controller.creates.Done()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.pending[pending.Session.ID()] = pending.Session
	controller.active = pending.Session.ID()
	controller.mu.Unlock()
	go controller.awaitCreatedSession(intent, pending)
	if err := controller.persistActive(ctx, pending.Session.ID()); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist starting active Telegram session: %w", err)
	}
	return controller.cardDecision(ctx, pending.Session.ID(), "")
}
func validatePendingCreate(pending PendingSessionStart, intent app.ConfirmedSessionIntent) error {
	session := pending.Session
	if pending.Outcome == nil {
		return errors.New("async session creator returned no terminal outcome")
	}
	if session.ID() == "" || session.IntentID() != intent.IntentID ||
		session.ComputerID() != intent.ComputerID || session.Provider() != intent.Provider ||
		session.Workdir() != intent.Workdir || session.Status() != domain.SessionStarting {
		return errors.New("async session creator returned an inconsistent durable starting session")
	}
	if _, hasBinding := session.Binding(); hasBinding {
		return errors.New("durable starting session unexpectedly has a provider binding")
	}
	return nil
}
func (controller *Controller) awaitCreatedSession(intent app.ConfirmedSessionIntent, pending PendingSessionStart) {
	defer controller.creates.Done()
	select {
	case <-controller.rootContext.Done():
		return
	case outcome, ok := <-pending.Outcome:
		if !ok {
			controller.asyncStartFailed(pending.Session.ID(), "Запуск сессии завершился без подтверждённого результата.", "")
			return
		}
		if outcome.Err != nil || !sameSessionIdentity(outcome.Session, pending.Session) {
			controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", "")
			return
		}
		binding, hasBinding := outcome.Session.Binding()
		switch outcome.Session.Status() {
		case domain.SessionReady:
			if !hasBinding || outcome.StartError != nil {
				controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", "")
				return
			}
			controller.mu.Lock()
			delete(controller.pending, outcome.Session.ID())
			controller.live[outcome.Session.ID()] = outcome.Session
			controller.ensureWorkerLocked(outcome.Session.ID())
			if !outcome.Replayed {
				controller.created[outcome.Session.ID()] = createdProcess{
					request: requestFromSession(outcome.Session),
					binding: binding,
				}
			}
			controller.mu.Unlock()
		case domain.SessionAwaitingRecovery:
			if hasBinding || outcome.Replayed || outcome.StartError == nil {
				controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", "")
				return
			}
			controller.mu.Lock()
			controller.pending[outcome.Session.ID()] = outcome.Session
			controller.mu.Unlock()
		default:
			controller.asyncStartFailed(pending.Session.ID(), "Не удалось подтвердить запуск сессии.", "")
		}
	}
}
func (controller *Controller) resumeArchivedAsync(ctx context.Context, sessionID domain.SessionID) (coordinator.Decision, error) {
	archived, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("load archived session before resume: %w", err)
	}
	prior, ok := archived.Binding()
	if archived.Status() != domain.SessionArchived || !ok {
		return coordinator.Decision{}, errors.New("semantic resume target is not an archived provider session")
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	previousActive := controller.active
	controller.creates.Add(1)
	controller.mu.Unlock()
	beginContext, cancelBegin := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(controller.rootContext, cancelBegin)
	pending, err := controller.asyncResumer.BeginResume(beginContext, sessionID)
	stopCancellation()
	cancelBegin()
	if err != nil {
		controller.creates.Done()
		return coordinator.Decision{}, fmt.Errorf("begin exact archive resume: %w", err)
	}
	if pending.Outcome == nil || !sameSessionIdentity(pending.Session, archived) || pending.Session.Status() != domain.SessionResuming {
		controller.creates.Done()
		return coordinator.Decision{}, errors.New("async archived resumer returned an inconsistent durable resuming session")
	}
	pendingBinding, hasBinding := pending.Session.Binding()
	if !hasBinding || pendingBinding != prior {
		controller.creates.Done()
		return coordinator.Decision{}, errors.New("durable resuming session lost the original provider binding")
	}
	controller.mu.Lock()
	controller.pending[sessionID] = pending.Session
	controller.active = sessionID
	controller.mu.Unlock()
	go controller.awaitResumedSession(archived, prior, previousActive, pending)
	if err := controller.persistActive(ctx, sessionID); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist resuming active Telegram session: %w", err)
	}
	return controller.cardDecision(ctx, sessionID, "")
}
func (controller *Controller) awaitResumedSession(
	archived domain.Session,
	prior domain.ProviderBinding,
	previousActive domain.SessionID,
	pending PendingSessionStart,
) {
	defer controller.creates.Done()
	select {
	case <-controller.rootContext.Done():
		return
	case outcome, ok := <-pending.Outcome:
		if !ok || outcome.Err != nil || !sameSessionIdentity(outcome.Session, archived) {
			controller.asyncStartFailed(archived.ID(), "Не удалось продолжить исходную сессию.", previousActive)
			return
		}
		binding, hasBinding := outcome.Session.Binding()
		if outcome.Session.Status() != domain.SessionReady || !hasBinding ||
			binding.Provider != prior.Provider || binding.SessionID != prior.SessionID ||
			binding.Generation <= prior.Generation || outcome.StartError != nil {
			controller.asyncStartFailed(archived.ID(), "Не удалось продолжить исходную сессию.", previousActive)
			return
		}
		priorCopy := prior
		controller.mu.Lock()
		delete(controller.pending, outcome.Session.ID())
		controller.live[outcome.Session.ID()] = outcome.Session
		controller.ensureWorkerLocked(outcome.Session.ID())
		controller.created[outcome.Session.ID()] = createdProcess{
			request: app.StartSessionRequest{
				SessionID: outcome.Session.ID(), ComputerID: outcome.Session.ComputerID(), Provider: outcome.Session.Provider(),
				Workdir: outcome.Session.Workdir(), Mode: app.SessionStartResume, PriorBinding: &priorCopy,
			},
			binding: binding,
		}
		controller.mu.Unlock()
	}
}
func (controller *Controller) asyncStartFailed(sessionID domain.SessionID, text string, previousActive domain.SessionID) {
	controller.mu.Lock()
	delete(controller.pending, sessionID)
	if controller.active == sessionID {
		controller.active = previousActive
	}
	controller.mu.Unlock()
	controller.notify(context.WithoutCancel(controller.rootContext), Notification{
		OperationID:    "session-start:" + string(sessionID),
		ConversationID: controller.ownerPrivateChatID,
		SessionID:      sessionID,
		Kind:           NotificationError,
		Text:           text,
	})
}
func sameSessionIdentity(left, right domain.Session) bool {
	return left.ID() != "" && left.ID() == right.ID() && left.IntentID() == right.IntentID() &&
		left.ComputerID() == right.ComputerID() && left.Provider() == right.Provider() &&
		left.Workdir() == right.Workdir()
}
func (controller *Controller) create(
	ctx context.Context,
	updateID int64,
	computerID domain.ComputerID,
	provider domain.Provider,
	workdir string,
) (coordinator.Decision, error) {
	if updateID <= 0 {
		return coordinator.Decision{}, errors.New("confirmed creation update id must be positive")
	}
	enabled, err := controller.providerEnabled(ctx, provider)
	if err != nil {
		return coordinator.Decision{}, err
	}
	if !enabled {
		return coordinator.Decision{}, errProviderUnavailable
	}
	if controller.asyncCreator != nil {
		return controller.createAsync(ctx, updateID, computerID, provider, workdir)
	}
	controller.mu.Lock()
	if controller.closed {
		controller.mu.Unlock()
		return coordinator.Decision{}, errors.New("Telegram controller is closed")
	}
	controller.creates.Add(1)
	controller.mu.Unlock()
	defer controller.creates.Done()
	createContext, cancelCreate := context.WithCancel(ctx)
	stopCancellation := context.AfterFunc(controller.rootContext, cancelCreate)
	defer func() {
		stopCancellation()
		cancelCreate()
	}()
	intent := app.ConfirmedSessionIntent{
		IntentID:   domain.IntentID("telegram-update:" + strconv.FormatInt(updateID, 10)),
		ComputerID: computerID,
		Provider:   provider,
		Workdir:    workdir,
	}
	result, err := controller.creator.Create(createContext, intent)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("create confirmed session: %w", err)
	}
	if result.Session.ID() == "" || result.Session.IntentID() != intent.IntentID ||
		result.Session.ComputerID() != computerID ||
		result.Session.Provider() != provider || result.Session.Workdir() != workdir {
		return coordinator.Decision{}, errors.New("session creator returned an inconsistent session")
	}
	binding, hasBinding := result.Session.Binding()
	if result.Replayed && result.StartError != nil {
		return coordinator.Decision{}, errors.New("replayed session contains a new start error")
	}
	switch result.Session.Status() {
	case domain.SessionAwaitingRecovery:
		if hasBinding || !result.Replayed && result.StartError == nil {
			return coordinator.Decision{}, errors.New("session creator returned inconsistent recovery state")
		}
	case domain.SessionReady:
		if !hasBinding || result.StartError != nil {
			return coordinator.Decision{}, errors.New("session creator returned inconsistent ready state")
		}
	default:
		return coordinator.Decision{}, errors.New("session creator returned a non-terminal creation state")
	}
	controller.mu.Lock()
	controller.active = result.Session.ID()
	if !result.Replayed && result.StartError == nil &&
		result.Session.Status() == domain.SessionReady && hasBinding {
		controller.live[result.Session.ID()] = result.Session
		controller.ensureWorkerLocked(result.Session.ID())
		controller.created[result.Session.ID()] = createdProcess{
			request: requestFromSession(result.Session),
			binding: binding,
		}
	}
	_, usable := controller.usableLocked(result.Session)
	controller.mu.Unlock()
	if err := controller.persistActive(ctx, result.Session.ID()); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist active Telegram session: %w", err)
	}
	switch result.Session.Status() {
	case domain.SessionAwaitingRecovery:
		return controller.cardDecision(ctx, result.Session.ID(), "")
	case domain.SessionReady:
		if !usable {
			return controller.cardDecision(ctx, result.Session.ID(), "")
		}
		return controller.cardDecision(ctx, result.Session.ID(), "")
	}
	return coordinator.Decision{}, errors.New("validated session status changed unexpectedly")
}
func (controller *Controller) persistActive(ctx context.Context, sessionID domain.SessionID) error {
	if controller.uiState == nil {
		return nil
	}
	return controller.uiState.SetActiveSession(ctx, sessionID)
}
func (controller *Controller) use(
	ctx context.Context,
	sessionID domain.SessionID,
) (coordinator.Decision, error) {
	session, err := controller.sessions.Load(ctx, sessionID)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("load selected session: %w", err)
	}
	controller.mu.Lock()
	_, usable := controller.usableLocked(session)
	controller.mu.Unlock()
	if !usable {
		if session.Status() == domain.SessionReady {
			return controller.cardDecision(ctx, sessionID, "")
		}
		return controller.status("Сессия " + string(sessionID) + " недоступна: " + string(session.Status()) + "."), nil
	}
	if err := controller.persistActive(ctx, sessionID); err != nil {
		return coordinator.Decision{}, fmt.Errorf("persist active Telegram session: %w", err)
	}
	controller.mu.Lock()
	controller.active = sessionID
	controller.mu.Unlock()
	return controller.cardDecision(ctx, sessionID, "")
}
func (controller *Controller) listSessions(ctx context.Context) (coordinator.Decision, error) {
	sessions, err := controller.sessions.List(ctx)
	if err != nil {
		return coordinator.Decision{}, fmt.Errorf("list sessions: %w", err)
	}
	if len(sessions) == 0 {
		return controller.menuStatus("Сессий нет."), nil
	}
	sort.Slice(sessions, func(left, right int) bool {
		return sessions[left].ID() < sessions[right].ID()
	})
	controller.mu.Lock()
	active := controller.active
	live := make(map[domain.SessionID]domain.Session, len(controller.live))
	for id, session := range controller.live {
		live[id] = session
	}
	controller.mu.Unlock()
	var text strings.Builder
	text.WriteString("Сессии\nВыберите сессию:")
	keyboard := coordinator.KeyboardMarkup{}
	row := []coordinator.KeyboardButton{}
	for _, session := range sessions {
		marker := ""
		if session.ID() == active {
			marker = "✓ "
		}
		label := marker + string(session.Provider()) + " " + shortID(session.ID())
		row = append(row, coordinator.KeyboardButton{Text: label, CallbackData: "sw:" + string(session.ID())})
		if len(row) == 3 {
			keyboard = append(keyboard, row)
			row = []coordinator.KeyboardButton{}
		}
		fmt.Fprintf(&text, "\n%s%s %s", marker, session.Provider(), session.Status())
		if session.Status() == domain.SessionReady {
			tracked, ok := live[session.ID()]
			if !ok || !tracked.Equal(session) {
				text.WriteString(" (процесс не запущен)")
			}
		}
	}
	if len(row) > 0 {
		keyboard = append(keyboard, row)
	}
	keyboard = append(keyboard, []coordinator.KeyboardButton{{Text: "+ Новая", CallbackData: "mm:new"}, {Text: "≡ Меню", CallbackData: "mm:back"}})
	decision := controller.status(text.String())
	decision.Keyboard = &keyboard
	return decision, nil
}
func (controller *Controller) enqueue(ctx context.Context, updateID int64, input PreparedInput) coordinator.Decision {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	return controller.enqueueSession(ctx, updateID, active, input)
}
func (controller *Controller) enqueueSession(ctx context.Context, updateID int64, sessionID domain.SessionID, input PreparedInput) coordinator.Decision {
	controller.mu.Lock()
	worker := controller.workers[sessionID]
	session, live := controller.live[sessionID]
	_, usable := controller.usableLocked(session)
	pending, isPending := controller.pending[sessionID]
	closed := controller.closed
	controller.mu.Unlock()
	if closed {
		return controller.status("Bria завершает работу.")
	}
	if controller.durableInput != nil {
		if sessionID == "" || (!live || !usable) && (!isPending || !acceptsDurableInput(pending.Status())) {
			return controller.status("Нет активной или запускаемой сессии для запроса.")
		}
		if updateID <= 0 {
			return controller.status("Не удалось определить сообщение для надёжного сохранения.")
		}
		messageID := "telegram-update:" + strconv.FormatInt(updateID, 10)
		receipt, err := controller.durableInput.Accept(ctx, SessionInput{
			SessionID:   sessionID,
			MessageID:   messageID,
			Payload:     []byte(input.Text),
			Attachments: append([]AttachmentRef(nil), input.Attachments...),
		})
		if err != nil {
			return controller.status("Не удалось надёжно сохранить запрос. Запрос не принят.")
		}
		if receipt.SessionID != sessionID || receipt.MessageID != messageID || receipt.Sequence == 0 {
			return controller.status("Не удалось подтвердить надёжное сохранение запроса. Запрос не принят.")
		}
		// The durable journal is the sole source for this user entry. Mirroring
		// it into the independently persisted card history here is not atomic:
		// a replay after a crash would either duplicate the entry or leave a
		// commit gap. Signed UI composition projects accepted inputs from the
		// durable source keyed by MessageID/Sequence.
		return controller.status("Запрос принят для сессии " + string(sessionID) + ".")
	}
	if len(input.Attachments) != 0 {
		return controller.status("Вложения требуют надёжного журнала и не приняты.")
	}
	if sessionID == "" || !live || !usable {
		return controller.status("Нет активной сессии с запущенным процессом.")
	}
	if worker == nil {
		return controller.status("Нет активной сессии с запущенным процессом.")
	}
	select {
	case worker.queue <- queuedTurn{text: input.Text, messageID: "telegram-update:" + strconv.FormatInt(updateID, 10)}:
		controller.appendUserHistory(sessionID, input.Text)
		return controller.status("Запрос принят для сессии " + string(sessionID) + ".")
	case <-controller.rootContext.Done():
		return controller.status("Bria завершает работу.")
	default:
		return controller.status("Очередь сессии " + string(sessionID) + " переполнена.")
	}
}
func acceptsDurableInput(status domain.SessionStatus) bool {
	switch status {
	case domain.SessionStarting, domain.SessionResuming, domain.SessionAwaitingRecovery:
		return true
	default:
		return false
	}
}

// ProcessDurableInput processes one exact leased journal input through the
// same turn/lifecycle path as the in-memory worker. The provider acceptance is
// not acknowledged in memory: OnAccepted must first commit durable custody.
func (controller *Controller) ProcessDurableInput(
	ctx context.Context,
	input DurableLeasedInput,
	callbacks DurableInputCallbacks,
) (DurableInputProcessReceipt, error) {
	receipt := DurableInputProcessReceipt{
		SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
	}
	prepared := PreparedInput{Text: string(input.Payload), Attachments: append([]AttachmentRef(nil), input.Attachments...)}
	if input.SessionID == "" || strings.TrimSpace(input.MessageID) == "" || input.Sequence == 0 ||
		!utf8.Valid(input.Payload) || validatePreparedInput(prepared) != nil {
		return receipt, errors.New("durable leased input is invalid")
	}
	if callbacks.OnAccepted == nil {
		return receipt, errors.New("durable input acceptance callback is required")
	}
	if _, ok := controller.submitter.(sessionruntime.InteractiveSubmitter); !ok {
		if _, structured := controller.submitter.(PreparedTurnSubmitter); !structured {
			return receipt, errors.New("provider does not expose exact durable acceptance")
		}
	}
	if len(input.Attachments) != 0 {
		if _, ok := controller.submitter.(PreparedTurnSubmitter); !ok {
			return receipt, errors.New("provider does not support structured attachments")
		}
		if controller.attachments == nil {
			return receipt, errors.New("attachment custody lifecycle is not configured")
		}
	}
	controller.mu.Lock()
	worker := controller.workers[input.SessionID]
	session, usable := controller.usableLocked(controller.live[input.SessionID])
	closed := controller.closed
	controller.mu.Unlock()
	if closed {
		return receipt, errors.New("Telegram controller is closed")
	}
	if worker == nil || !usable || session.ID() != input.SessionID {
		return receipt, errors.New("durable input session is not live")
	}
	binding, hasBinding := session.Binding()
	if len(input.Attachments) != 0 && (!hasBinding || strings.TrimSpace(binding.SessionID) == "") {
		return receipt, errors.New("attachment session has no exact provider binding")
	}
	if worker.hasActiveTurn() {
		return receipt, errors.New("durable input session already has an active turn")
	}
	acceptedOnce := false
	completion, accepted := worker.runTurnWithAcceptance(ctx, queuedTurn{
		text: string(input.Payload), messageID: input.MessageID, attachments: append([]AttachmentRef(nil), input.Attachments...),
	}, func(callbackCtx context.Context) error {
		if acceptedOnce {
			return errors.New("provider repeated durable acceptance")
		}
		acceptedOnce = true
		if err := callbacks.OnAccepted(callbackCtx, DurableInputAcceptance{
			SessionID: input.SessionID, MessageID: input.MessageID, Sequence: input.Sequence,
		}); err != nil {
			return err
		}
		return nil
	})
	receipt.Accepted = accepted
	receipt.Completion = completion
	if !accepted {
		return receipt, errors.New("provider did not durably accept input")
	}
	if err := turnprocessing.CompleteAttachments(ctx, controller.attachments, turnprocessing.Request{
		SessionID: input.SessionID, ProviderSessionID: binding.SessionID, MessageID: input.MessageID,
		Input: PreparedInput{Text: string(input.Payload), Attachments: append([]AttachmentRef(nil), input.Attachments...)},
	}); err != nil {
		receipt.Completion = DurableInputFailed
		return receipt, errors.New("complete attachment custody")
	}
	return receipt, nil
}
func (controller *Controller) appendUserHistory(sessionID domain.SessionID, text string) {
	entry := "Вы: " + strings.TrimSpace(text)
	controller.mu.Lock()
	controller.history[sessionID] = append(controller.history[sessionID], entry)
	controller.mu.Unlock()
	if historyStore, ok := controller.uiState.(CardHistoryStore); ok {
		_ = historyStore.AppendCardHistory(controller.rootContext, sessionID, entry)
	}
}
func (controller *Controller) stopCurrent(ctx context.Context) coordinator.Decision {
	controller.mu.Lock()
	active := controller.active
	controller.mu.Unlock()
	return controller.stopSession(ctx, active)
}
func (controller *Controller) stopSession(ctx context.Context, sessionID domain.SessionID) coordinator.Decision {
	controller.mu.Lock()
	worker := controller.workers[sessionID]
	controller.mu.Unlock()
	active := sessionID
	if active == "" || worker == nil {
		return controller.status("Нет выполняющегося запроса.")
	}
	turn, activeTurn, claimed := worker.claimConfirmedStop()
	if !activeTurn {
		return controller.status("Нет выполняющегося запроса.")
	}
	if !claimed {
		return controller.status("Остановка запроса уже выполняется для сессии " + string(active) + ".")
	}
	if controller.turnLifecycle != nil {
		stopping, err := controller.turnLifecycle.BeginStop(ctx, active)
		if err != nil {
			worker.releaseFailedStop(turn)
			return controller.status("Не удалось сохранить остановку запроса для сессии " + string(active) + ".")
		}
		controller.replaceLive(stopping)
	}
	if controller.stopper == nil {
		if !worker.cancelClaimedTurn(turn) {
			worker.releaseFailedStop(turn)
			return controller.status("Нет выполняющегося запроса.")
		}
		return controller.status("Остановка запроса отправлена для сессии " + string(active) + ".")
	}
	if err := controller.stopper.StopCurrent(ctx, active); err != nil {
		worker.releaseFailedStop(turn)
		return controller.status("Не удалось подтвердить остановку запроса для сессии " + string(active) + ".")
	}
	return controller.status("Запрос остановлен для сессии " + string(active) + ".")
}
func (controller *Controller) ensureWorkerLocked(sessionID domain.SessionID) *sessionWorker {
	if existing := controller.workers[sessionID]; existing != nil {
		return existing
	}
	worker := &sessionWorker{
		controller: controller,
		sessionID:  sessionID,
		queue:      make(chan queuedTurn, controller.queueLimit),
	}
	controller.workers[sessionID] = worker
	controller.worker.Add(1)
	go func() {
		defer controller.worker.Done()
		worker.run()
	}()
	return worker
}
func (controller *Controller) usableLocked(session domain.Session) (domain.Session, bool) {
	if session.ID() == "" {
		return domain.Session{}, false
	}
	switch session.Status() {
	case domain.SessionReady, domain.SessionRunning, domain.SessionStopping:
	default:
		return domain.Session{}, false
	}
	live, ok := controller.live[session.ID()]
	return live, ok && live.Equal(session)
}
func (controller *Controller) replaceLive(session domain.Session) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if _, exists := controller.live[session.ID()]; exists {
		controller.live[session.ID()] = session
	}
}
func (worker *sessionWorker) run() {
	for {
		select {
		case <-worker.controller.rootContext.Done():
			return
		default:
		}
		select {
		case <-worker.controller.rootContext.Done():
			return
		case turn := <-worker.queue:
			worker.runTurn(turn)
		}
	}
}
func (worker *sessionWorker) runTurn(turn queuedTurn) {
	_, _ = worker.runTurnWithAcceptance(worker.controller.rootContext, turn, nil)
}
func (worker *sessionWorker) runTurnWithAcceptance(
	ctx context.Context,
	turn queuedTurn,
	onAccepted func(context.Context) error,
) (DurableInputCompletion, bool) {
	turnContext, cancelTurn := context.WithCancel(ctx)
	worker.mu.Lock()
	worker.activeTurn++
	activeTurn := worker.activeTurn
	worker.stoppingTurn = 0
	worker.activeCancel = cancelTurn
	worker.mu.Unlock()
	if worker.controller.turnLifecycle != nil {
		running, err := worker.controller.turnLifecycle.Start(turnContext, worker.sessionID)
		if err != nil {
			cancelTurn()
			worker.clearActiveTurn(activeTurn)
			worker.notifyTurnError(turn.messageID+":lifecycle-start", "Не удалось сохранить начало запроса.")
			return DurableInputFailed, false
		}
		worker.controller.replaceLive(running)
	}
	eventIndex := 0
	worker.controller.mu.Lock()
	current := worker.controller.live[worker.sessionID]
	worker.controller.mu.Unlock()
	binding, _ := current.Binding()
	execution, err := turnprocessing.Execute(turnContext, worker.controller.submitter, worker.controller.interactions, worker.controller.attachments,
		turnprocessing.Request{
			SessionID: worker.sessionID, ProviderSessionID: binding.SessionID, MessageID: turn.messageID,
			Input: PreparedInput{Text: turn.text, Attachments: append([]AttachmentRef(nil), turn.attachments...)},
		}, turnprocessing.Callbacks{
			MarkInputAccepted: onAccepted,
			AfterAccepted: func() {
				if onAccepted != nil {
					worker.controller.appendUserHistory(worker.sessionID, turn.text)
				}
			},
			OnEvent: func(event sessionruntime.TurnEvent) error {
				eventIndex++
				worker.emitTurnEvent(turn.messageID, eventIndex, event)
				return nil
			},
		})
	result, accepted, streamedEvents := execution.Result, execution.Accepted, execution.StreamedEvents
	cancelTurn()
	worker.clearActiveTurn(activeTurn)
	if worker.controller.turnLifecycle != nil {
		finishContext := context.WithoutCancel(worker.controller.rootContext)
		finished, closeAfter, finishErr := worker.controller.turnLifecycle.Finish(finishContext, worker.sessionID)
		if finishErr != nil {
			worker.notifyTurnError(turn.messageID+":lifecycle-finish", "Не удалось сохранить завершение запроса.")
			return DurableInputFailed, accepted
		}
		worker.controller.replaceLive(finished)
		if closeAfter {
			if worker.controller.sessionCloser == nil {
				worker.notifyTurnError(turn.messageID+":close-missing", "Сессия ожидает закрытия, но обработчик закрытия не настроен.")
				return DurableInputFailed, accepted
			}
			closed, closeErr := worker.controller.sessionCloser.Close(finishContext, worker.sessionID)
			if closeErr != nil {
				worker.notifyTurnError(turn.messageID+":close-error", "Не удалось подтвердить закрытие сессии.")
				return DurableInputFailed, accepted
			}
			worker.controller.applyClosedSession(closed.Session)
		}
	}
	if err != nil || result.TerminalStatus != sessionruntime.StatusCompleted {
		errorText := "Ошибка исполнителя: запрос не выполнен."
		if result.ErrorCode == sessionruntime.ErrorAuthenticationFailed {
			errorText = "Ошибка авторизации Claude: требуется выполнить вход (/login)."
		}
		worker.controller.mu.Lock()
		worker.controller.history[worker.sessionID] = append(worker.controller.history[worker.sessionID], errorText)
		worker.controller.mu.Unlock()
		if historyStore, ok := worker.controller.uiState.(CardHistoryStore); ok {
			_ = historyStore.AppendCardHistory(worker.controller.rootContext, worker.sessionID, errorText)
		}
		worker.controller.notify(worker.controller.rootContext, Notification{
			OperationID:    turn.messageID + ":error",
			ConversationID: worker.controller.ownerPrivateChatID,
			SessionID:      worker.sessionID,
			Kind:           NotificationError,
			Text:           errorText,
		})
		return DurableInputFailed, accepted
	}
	if !streamedEvents {
		for eventIndex, event := range result.Events {
			worker.emitTurnEvent(turn.messageID, eventIndex+1, event)
		}
	}
	if worker.controller.finals != nil {
		if err := worker.controller.finals.ProcessFinal(context.WithoutCancel(worker.controller.rootContext), FinalObservation{
			OperationID: turn.messageID + ":final", SessionID: worker.sessionID, MessageID: turn.messageID, Text: result.Final,
		}); err != nil {
			worker.notifyTurnError(turn.messageID+":final-processor-error", "Не удалось обработать итоговые артефакты.")
		}
	}
	worker.controller.mu.Lock()
	worker.controller.history[worker.sessionID] = append(worker.controller.history[worker.sessionID], result.Final)
	worker.controller.mu.Unlock()
	if result.Final != "" {
		if historyStore, ok := worker.controller.uiState.(CardHistoryStore); ok {
			_ = historyStore.AppendCardHistory(worker.controller.rootContext, worker.sessionID, result.Final)
		}
	}
	worker.controller.notify(worker.controller.rootContext, Notification{
		OperationID:    turn.messageID + ":final",
		ConversationID: worker.controller.ownerPrivateChatID,
		SessionID:      worker.sessionID,
		Kind:           NotificationFinal,
		Text:           result.Final,
	})
	return DurableInputSucceeded, accepted
}
func (worker *sessionWorker) emitTurnEvent(messageID string, eventIndex int, event sessionruntime.TurnEvent) {
	worker.controller.mu.Lock()
	worker.controller.history[worker.sessionID] = append(worker.controller.history[worker.sessionID], event.Text)
	worker.controller.mu.Unlock()
	if historyStore, ok := worker.controller.uiState.(CardHistoryStore); ok {
		_ = historyStore.AppendCardHistory(worker.controller.rootContext, worker.sessionID, event.Text)
	}
	kind := NotificationKind("")
	switch event.Kind {
	case sessionruntime.EventCommentary:
		kind = NotificationCommentary
	case sessionruntime.EventQuestion:
		kind = NotificationQuestion
	default:
		return
	}
	operationID := messageID + ":event:" + strconv.Itoa(eventIndex)
	if worker.controller.runtimeEvents != nil {
		if err := worker.controller.runtimeEvents.ObserveRuntimeEvent(context.WithoutCancel(worker.controller.rootContext), RuntimeEventObservation{
			OperationID: operationID, SessionID: worker.sessionID, MessageID: messageID, EventIndex: eventIndex, Event: event,
		}); err != nil {
			worker.notifyTurnError(operationID+":observer-error", "Не удалось обновить Screen для события исполнителя.")
		}
	}
	worker.controller.notify(worker.controller.rootContext, Notification{
		OperationID:    operationID,
		ConversationID: worker.controller.ownerPrivateChatID,
		SessionID:      worker.sessionID,
		Kind:           kind,
		Text:           event.Text,
	})
}
func (worker *sessionWorker) cancelClaimedTurn(turn uint64) bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeCancel == nil || worker.activeTurn != turn || worker.stoppingTurn != turn {
		return false
	}
	worker.activeCancel()
	return true
}
func (worker *sessionWorker) clearActiveTurn(turn uint64) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeTurn == turn {
		worker.activeCancel = nil
		worker.stoppingTurn = 0
	}
}
func (worker *sessionWorker) hasActiveTurn() bool {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	return worker.activeCancel != nil
}
func (worker *sessionWorker) notifyTurnError(operationID, text string) {
	worker.controller.notify(context.WithoutCancel(worker.controller.rootContext), Notification{
		OperationID:    operationID,
		ConversationID: worker.controller.ownerPrivateChatID,
		SessionID:      worker.sessionID,
		Kind:           NotificationError,
		Text:           text,
	})
}
func (worker *sessionWorker) claimConfirmedStop() (turn uint64, active, claimed bool) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeCancel == nil {
		return 0, false, false
	}
	if worker.stoppingTurn == worker.activeTurn {
		return worker.activeTurn, true, false
	}
	worker.stoppingTurn = worker.activeTurn
	return worker.activeTurn, true, true
}
func (worker *sessionWorker) releaseFailedStop(turn uint64) {
	worker.mu.Lock()
	defer worker.mu.Unlock()
	if worker.activeTurn == turn && worker.stoppingTurn == turn {
		worker.stoppingTurn = 0
	}
}

// Close prevents new work, cancels every controller worker, and, when a
// Lifecycle is configured, aborts only non-replayed processes created through
// this controller instance. Persisted sessions merely loaded after restart are
// never aborted.
func (controller *Controller) Close(ctx context.Context) error {
	controller.mu.Lock()
	if controller.closed {
		done := controller.closeDone
		controller.mu.Unlock()
		select {
		case <-done:
			controller.mu.Lock()
			err := controller.closeErr
			controller.mu.Unlock()
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	controller.closed = true
	controller.cancelRoot()
	controller.mu.Unlock()
	err := controller.closeWithin(ctx)
	controller.mu.Lock()
	controller.closeErr = err
	close(controller.closeDone)
	controller.mu.Unlock()
	return err
}

type abortResult struct {
	index int
	err   error
}

func (controller *Controller) closeWithin(ctx context.Context) error {
	createsDone := make(chan struct{})
	go func() {
		controller.creates.Wait()
		close(createsDone)
	}()
	select {
	case <-createsDone:
	case <-ctx.Done():
		return ctx.Err()
	}
	controller.mu.Lock()
	created := make([]createdProcess, 0, len(controller.created))
	for _, process := range controller.created {
		created = append(created, process)
	}
	controller.mu.Unlock()
	sort.Slice(created, func(left, right int) bool {
		return created[left].request.SessionID < created[right].request.SessionID
	})
	abortResults := make(chan abortResult, len(created))
	abortPending := 0
	if controller.lifecycle != nil {
		abortPending = len(created)
		for index, process := range created {
			go func() {
				abortResults <- abortResult{
					index: index,
					err:   controller.lifecycle.Abort(ctx, process.request, process.binding),
				}
			}()
		}
	}
	workersDone := make(chan struct{})
	go func() {
		controller.worker.Wait()
		close(workersDone)
	}()
	workersPending := true
	abortErrors := make([]error, len(created))
	for abortPending > 0 || workersPending {
		select {
		case result := <-abortResults:
			abortPending--
			abortErrors[result.index] = result.err
		case <-workersDone:
			workersPending = false
			workersDone = nil
		case <-ctx.Done():
			return errors.Join(append(abortErrors, ctx.Err())...)
		}
	}
	return errors.Join(abortErrors...)
}
func requestFromSession(session domain.Session) app.StartSessionRequest {
	return app.StartSessionRequest{
		SessionID: session.ID(), ComputerID: session.ComputerID(),
		Provider: session.Provider(), Workdir: session.Workdir(),
	}
}
func parseNew(text string) (domain.Provider, string, bool) {
	parts := strings.SplitN(text, " ", 3)
	if len(parts) != 3 || parts[0] != "/new" {
		return "", "", false
	}
	workdir := strings.TrimSpace(parts[2])
	if workdir == "" || !filepath.IsAbs(workdir) {
		return "", "", false
	}
	provider := domain.Provider(parts[1])
	if provider != domain.ProviderCodex && provider != domain.ProviderClaude {
		return "", "", false
	}
	return provider, workdir, true
}
func parseUse(text string) (domain.SessionID, bool) {
	parts := strings.Fields(text)
	if len(parts) != 2 || parts[0] != "/use" || !canonicalUUID(parts[1]) {
		return "", false
	}
	return domain.SessionID(parts[1]), true
}
func canonicalUUID(value string) bool {
	if len(value) != 36 || value != strings.ToLower(value) ||
		value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
		return false
	}
	for index, character := range value {
		if index == 8 || index == 13 || index == 18 || index == 23 {
			continue
		}
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
