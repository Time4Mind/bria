package telegramapp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/runtimehost"
	"github.com/Time4Mind/bria/internal/sessionstart"
	"github.com/Time4Mind/bria/internal/telegrambot"
	"github.com/Time4Mind/bria/internal/telegramui"
	"github.com/Time4Mind/bria/internal/workspace"
)

const (
	createDirectoriesPerPage = 6
	createResumePerPage      = 8
)

type createFlow struct {
	id          string
	expiresAt   time.Time
	nodes       []application.NodeItem
	nodeID      domain.NodeID
	backend     string
	backends    []string
	fromNode    bool
	workdir     string
	dirs        []workspace.Directory
	candidates  []sessionstart.ProviderCandidate
	page        int
	resumePage  int
	resumeTotal int
}

type SessionStarter interface {
	Browse(context.Context, application.Principal, domain.NodeID, string) (sessionstart.BrowseResult, error)
	Discover(
		context.Context, application.Principal, domain.NodeID, string, string, int, int,
	) (sessionstart.ProviderPage, error)
	Create(context.Context, application.Principal, application.CreateSessionRequest) (domain.Session, error)
}

func (h *Handler) SetSessionStarter(starter SessionStarter) error {
	if starter == nil {
		return errors.New("session starter is required")
	}
	h.starter = starter
	return nil
}

func isCreateAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionNewSession, telegramui.ActionNewNode, telegramui.ActionNewBackend,
		telegramui.ActionNewDirectory, telegramui.ActionNewDirectoryUp,
		telegramui.ActionNewDirectoryPick, telegramui.ActionNewDirectoryBack,
		telegramui.ActionNewDirectoryPrev, telegramui.ActionNewDirectoryFirst,
		telegramui.ActionNewDirectoryNext,
		telegramui.ActionNewResume, telegramui.ActionNewResumePrevious,
		telegramui.ActionNewResumeFirst, telegramui.ActionNewResumeNext,
		telegramui.ActionNewFresh:
		return true
	default:
		return false
	}
}

func (h *Handler) handleCreateCallback(
	ctx context.Context,
	actor application.Principal,
	update telegrambot.IncomingUpdate,
	callback telegramui.Callback,
) error {
	if h.starter == nil {
		return nil
	}
	screen, ref, err := h.createScreen(ctx, actor, callback)
	if safeDrop(err) {
		return nil
	}
	if err != nil {
		return err
	}
	edited, err := h.messenger.EditScreen(ctx, update.CallbackOrigin, screen)
	if err == nil && ref.SessionID != "" {
		h.schedulePaneRefresh(ctx, actor, ref, edited)
	}
	return err
}

func (h *Handler) createScreen(
	ctx context.Context,
	actor application.Principal,
	callback telegramui.Callback,
) (telegramui.Screen, domain.SessionRef, error) {
	if callback.Action == telegramui.ActionNewSession {
		flow, err := h.newCreateFlow(actor)
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		if callback.Token != "" {
			flow.fromNode = true
			nodes := make([]domain.NodeID, 0, len(flow.nodes))
			for _, item := range flow.nodes {
				nodes = append(nodes, item.Node.ID)
			}
			nodeID, resolveErr := h.tokens.ResolveNode(
				actor.UserID, telegramui.ActionNewSession, callback.Token, nodes,
			)
			if resolveErr != nil {
				return telegramui.Screen{}, domain.SessionRef{}, resolveErr
			}
			for _, item := range flow.nodes {
				if item.Node.ID == nodeID {
					return h.beginCreateOnNode(ctx, actor, flow, item)
				}
			}
			return telegramui.Screen{}, domain.SessionRef{}, domain.ErrNotFound
		}
		preferences, err := h.service.Preferences(actor)
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		if preferences.SessionView == domain.ViewHostFirst {
			selected, found, selectErr := h.service.SelectedNode(actor)
			if selectErr != nil {
				return telegramui.Screen{}, domain.SessionRef{}, selectErr
			}
			if found && selected.Node.Status == domain.NodeOnline {
				return h.beginCreateOnNode(ctx, actor, flow, selected)
			}
		}
		return h.renderCreateNodes(actor, flow)
	}
	flow, err := h.activeCreateFlow(actor.UserID)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	switch callback.Action {
	case telegramui.ActionNewNode:
		return h.chooseCreateNode(ctx, actor, flow, callback.Token)
	case telegramui.ActionNewBackend:
		return h.chooseCreateBackend(ctx, actor, flow, callback.Token)
	case telegramui.ActionNewDirectory:
		return h.chooseDirectory(ctx, actor, flow, callback.Token)
	case telegramui.ActionNewDirectoryUp:
		return h.browseDirectory(ctx, actor, flow, filepath.Dir(flow.workdir))
	case telegramui.ActionNewDirectoryPrev:
		flow.page = previousPage(flow.page, h.directoryPages(flow))
		return h.renderCreateDirectories(actor, flow)
	case telegramui.ActionNewDirectoryFirst:
		flow.page = 0
		return h.renderCreateDirectories(actor, flow)
	case telegramui.ActionNewDirectoryNext:
		flow.page = nextPage(flow.page, h.directoryPages(flow))
		return h.renderCreateDirectories(actor, flow)
	case telegramui.ActionNewDirectoryPick:
		return h.discoverCreateSessions(ctx, actor, flow)
	case telegramui.ActionNewDirectoryBack:
		return h.renderCreateDirectories(actor, flow)
	case telegramui.ActionNewResume:
		providerID, resolveErr := h.resolveProviderChoice(actor, flow, callback.Token)
		if resolveErr != nil {
			return telegramui.Screen{}, domain.SessionRef{}, resolveErr
		}
		return h.finishCreate(ctx, actor, flow, providerID)
	case telegramui.ActionNewResumePrevious:
		flow.resumePage = previousPage(flow.resumePage, h.resumePages(flow))
		return h.loadCreateResumePage(ctx, actor, flow)
	case telegramui.ActionNewResumeFirst:
		flow.resumePage = 0
		return h.loadCreateResumePage(ctx, actor, flow)
	case telegramui.ActionNewResumeNext:
		flow.resumePage = nextPage(flow.resumePage, h.resumePages(flow))
		return h.loadCreateResumePage(ctx, actor, flow)
	case telegramui.ActionNewFresh:
		return h.finishCreate(ctx, actor, flow, "")
	default:
		return telegramui.Screen{}, domain.SessionRef{}, domain.ErrNotFound
	}
}

func previousPage(page, pages int) int {
	if pages <= 1 {
		return 0
	}
	return (page - 1 + pages) % pages
}

func nextPage(page, pages int) int {
	if pages <= 1 {
		return 0
	}
	return (page + 1) % pages
}

func (h *Handler) newCreateFlow(actor application.Principal) (*createFlow, error) {
	nodes, err := h.service.ListNodes(actor)
	if err != nil {
		return nil, err
	}
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	enabled := nodes[:0]
	for _, item := range nodes {
		if item.Node.Enabled() {
			enabled = append(enabled, item)
		}
	}
	flow := &createFlow{
		id: hex.EncodeToString(raw), expiresAt: time.Now().Add(h.flowTTL), nodes: enabled,
	}
	h.paneMu.Lock()
	h.createFlows[actor.UserID] = flow
	h.paneMu.Unlock()
	return flow, nil
}

func (h *Handler) activeCreateFlow(userID domain.UserID) (*createFlow, error) {
	h.paneMu.Lock()
	defer h.paneMu.Unlock()
	flow := h.createFlows[userID]
	if flow == nil || time.Now().After(flow.expiresAt) {
		delete(h.createFlows, userID)
		return nil, domain.ErrNotFound
	}
	flow.expiresAt = time.Now().Add(h.flowTTL)
	return flow, nil
}

func (h *Handler) clearCreateFlow(userID domain.UserID) {
	h.paneMu.Lock()
	delete(h.createFlows, userID)
	h.paneMu.Unlock()
}

func (h *Handler) renderCreateNodes(actor application.Principal, flow *createFlow) (telegramui.Screen, domain.SessionRef, error) {
	items := make([]telegramui.CreateNodeItem, 0, len(flow.nodes))
	for _, item := range flow.nodes {
		token, err := h.tokens.Choice(actor.UserID, telegramui.ActionNewNode, flow.id, string(item.Node.ID))
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		items = append(items, telegramui.CreateNodeItem{
			Token: token, Name: item.Node.Name, Status: createNodeStatus(item.Node.Status),
		})
	}
	return telegramui.RenderCreateNodes(h.copy(actor), items), domain.SessionRef{}, nil
}

func createNodeStatus(status domain.NodeStatus) telegramui.NodeStatus {
	if status == domain.NodeOnline {
		return telegramui.NodeOnline
	}
	if status == domain.NodeReconnecting {
		return telegramui.NodeReconnecting
	}
	return telegramui.NodeOffline
}

func (h *Handler) chooseCreateNode(ctx context.Context, actor application.Principal, flow *createFlow, token telegramui.OpaqueToken) (telegramui.Screen, domain.SessionRef, error) {
	values := make([]string, len(flow.nodes))
	for index, item := range flow.nodes {
		values[index] = string(item.Node.ID)
	}
	value, err := h.tokens.ResolveChoice(actor.UserID, telegramui.ActionNewNode, flow.id, token, values)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	for _, item := range flow.nodes {
		if string(item.Node.ID) != value {
			continue
		}
		return h.beginCreateOnNode(ctx, actor, flow, item)
	}
	return telegramui.Screen{}, domain.SessionRef{}, domain.ErrNotFound
}

func (h *Handler) beginCreateOnNode(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
	item application.NodeItem,
) (telegramui.Screen, domain.SessionRef, error) {
	if !item.Node.Enabled() || item.Node.Status != domain.NodeOnline {
		return telegramui.Screen{}, domain.SessionRef{}, domain.ErrNotFound
	}
	flow.nodeID = item.Node.ID
	backends := createBackends(item.Node)
	if len(backends) == 0 {
		choices := make([]telegramui.CreateChoice, 0, len(item.Node.InstalledBackends))
		for _, backend := range item.Node.InstalledBackends {
			name := strings.ToLower(strings.TrimSpace(backend.Name))
			if name == "" || !backendSupportsCreate(backend) {
				continue
			}
			token, tokenErr := h.tokens.Choice(
				actor.UserID, telegramui.ActionBackendConnect, "node_backend",
				string(item.Node.ID)+"\x00"+name,
			)
			if tokenErr != nil {
				return telegramui.Screen{}, domain.SessionRef{}, tokenErr
			}
			choices = append(choices, telegramui.CreateChoice{Token: token, Label: name})
		}
		return telegramui.RenderCreateNoBackends(
			h.copy(actor), item.Node.Name, choices,
		), domain.SessionRef{}, nil
	}
	if len(backends) == 1 {
		flow.backend = backends[0]
		return h.browseDirectory(ctx, actor, flow, "")
	}
	flow.backends = backends
	return h.renderCreateBackends(actor, flow, item.Node.Name, backends)
}

func createBackends(node domain.Node) []string {
	return createBackendDescriptors(node.Backends)
}

func createBackendDescriptors(backends []domain.BackendDescriptor) []string {
	result := make([]string, 0, len(backends))
	for _, backend := range backends {
		if backendSupportsCreate(backend) {
			result = append(result, strings.ToLower(backend.Name))
		}
	}
	sort.Strings(result)
	return result
}

func backendSupportsCreate(backend domain.BackendDescriptor) bool {
	for _, capability := range backend.Capabilities {
		if capability == string(runtimehost.CapabilitySessionCreate) {
			return true
		}
	}
	return false
}

func (h *Handler) renderCreateBackends(actor application.Principal, flow *createFlow, nodeName string, backends []string) (telegramui.Screen, domain.SessionRef, error) {
	choices := make([]telegramui.CreateChoice, 0, len(backends))
	for _, backend := range backends {
		token, err := h.tokens.Choice(actor.UserID, telegramui.ActionNewBackend, flow.id, backend)
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		choices = append(choices, telegramui.CreateChoice{Token: token, Label: backend})
	}
	if flow.fromNode {
		return telegramui.RenderCreateBackendsWithBack(
			h.copy(actor), nodeName, choices, telegramui.ActionSessions, "",
		), domain.SessionRef{}, nil
	}
	return telegramui.RenderCreateBackends(h.copy(actor), nodeName, choices), domain.SessionRef{}, nil
}
