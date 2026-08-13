package telegramapp

import (
	"context"
	"strings"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func (h *Handler) chooseCreateBackend(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
	token telegramui.OpaqueToken,
) (telegramui.Screen, domain.SessionRef, error) {
	backend, err := h.tokens.ResolveChoice(
		actor.UserID, telegramui.ActionNewBackend, flow.id, token, flow.backends,
	)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	flow.backend = backend
	return h.browseDirectory(ctx, actor, flow, "")
}

func (h *Handler) browseDirectory(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
	path string,
) (telegramui.Screen, domain.SessionRef, error) {
	result, err := h.starter.Browse(ctx, actor, flow.nodeID, path)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	flow.workdir = result.Path
	flow.dirs = result.Directories
	flow.candidates = nil
	flow.page = 0
	return h.renderCreateDirectories(actor, flow)
}

func (h *Handler) chooseDirectory(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
	token telegramui.OpaqueToken,
) (telegramui.Screen, domain.SessionRef, error) {
	values := make([]string, len(flow.dirs))
	for index, directory := range flow.dirs {
		values[index] = directory.Path
	}
	path, err := h.tokens.ResolveChoice(
		actor.UserID, telegramui.ActionNewDirectory, flow.id, token, values,
	)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	return h.browseDirectory(ctx, actor, flow, path)
}

func (h *Handler) renderCreateDirectories(
	actor application.Principal,
	flow *createFlow,
) (telegramui.Screen, domain.SessionRef, error) {
	pages := h.directoryPages(flow)
	if flow.page >= pages {
		flow.page = pages - 1
	}
	start := flow.page * createDirectoriesPerPage
	end := min(len(flow.dirs), start+createDirectoriesPerPage)
	choices := make([]telegramui.CreateChoice, 0, end-start)
	for _, directory := range flow.dirs[start:end] {
		token, err := h.tokens.Choice(
			actor.UserID, telegramui.ActionNewDirectory, flow.id, directory.Path,
		)
		if err != nil {
			return telegramui.Screen{}, domain.SessionRef{}, err
		}
		choices = append(choices, telegramui.CreateChoice{Token: token, Label: directory.Name})
	}
	screen := telegramui.RenderCreateDirectories(
		h.copy(actor), flow.workdir, choices, flow.page+1, pages,
	)
	return screen, domain.SessionRef{}, nil
}

func (h *Handler) directoryPages(flow *createFlow) int {
	return max(1, (len(flow.dirs)+createDirectoriesPerPage-1)/createDirectoriesPerPage)
}

func (h *Handler) discoverCreateSessions(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
) (telegramui.Screen, domain.SessionRef, error) {
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	if preferences.SkipResumeSelection {
		return h.finishCreate(ctx, actor, flow, "")
	}
	flow.resumePage = 0
	return h.loadCreateResumePage(ctx, actor, flow)
}

func (h *Handler) loadCreateResumePage(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
) (telegramui.Screen, domain.SessionRef, error) {
	page, err := h.starter.Discover(
		ctx, actor, flow.nodeID, flow.backend, flow.workdir,
		flow.resumePage*createResumePerPage, createResumePerPage,
	)
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	if page.Total == 0 {
		return h.finishCreate(ctx, actor, flow, "")
	}
	if len(page.Items) == 0 && flow.resumePage > 0 {
		flow.resumePage = (page.Total - 1) / createResumePerPage
		return h.loadCreateResumePage(ctx, actor, flow)
	}
	flow.candidates = page.Items
	flow.resumeTotal = page.Total
	return h.renderCreateResume(actor, flow)
}

func (h *Handler) renderCreateResume(
	actor application.Principal,
	flow *createFlow,
) (telegramui.Screen, domain.SessionRef, error) {
	pages := h.resumePages(flow)
	if flow.resumePage >= pages {
		flow.resumePage = pages - 1
	}
	offset := flow.resumePage * createResumePerPage
	choices := make([]telegramui.CreateChoice, 0, len(flow.candidates))
	for _, item := range flow.candidates {
		token, tokenErr := h.tokens.Choice(
			actor.UserID, telegramui.ActionNewResume, flow.id, item.ID,
		)
		if tokenErr != nil {
			return telegramui.Screen{}, domain.SessionRef{}, tokenErr
		}
		label := strings.Join(strings.Fields(item.Summary), " ")
		if label == "" {
			label = h.copy(actor).Text(i18n.NewUntitledSession)
		}
		label += " · " + relativeCreateTime(h.copy(actor), item.UpdatedAt)
		choices = append(choices, telegramui.CreateChoice{Token: token, Label: label})
	}
	return telegramui.RenderCreateResumePage(
		h.copy(actor), flow.workdir, choices, offset, flow.resumePage+1, pages,
	), domain.SessionRef{}, nil
}

func (h *Handler) resumePages(flow *createFlow) int {
	return max(1, (flow.resumeTotal+createResumePerPage-1)/createResumePerPage)
}

func (h *Handler) resolveProviderChoice(
	actor application.Principal,
	flow *createFlow,
	token telegramui.OpaqueToken,
) (string, error) {
	values := make([]string, len(flow.candidates))
	for index, item := range flow.candidates {
		values[index] = item.ID
	}
	return h.tokens.ResolveChoice(actor.UserID, telegramui.ActionNewResume, flow.id, token, values)
}

func (h *Handler) finishCreate(
	ctx context.Context,
	actor application.Principal,
	flow *createFlow,
	providerID string,
) (telegramui.Screen, domain.SessionRef, error) {
	session, err := h.starter.Create(ctx, actor, application.CreateSessionRequest{
		NodeID: flow.nodeID, Backend: flow.backend, Workdir: flow.workdir,
		ProviderSessionID: providerID,
	})
	if err != nil {
		return telegramui.Screen{}, domain.SessionRef{}, err
	}
	h.paneMu.Lock()
	delete(h.createFlows, actor.UserID)
	h.paneMu.Unlock()
	screen, err := h.renderSessionCard(ctx, actor, session.Ref(), 0)
	return screen, session.Ref(), err
}

func relativeCreateTime(copy i18n.Localizer, at time.Time) string {
	if at.IsZero() {
		return "—"
	}
	age := time.Since(at)
	if age < time.Minute {
		return copy.Text(i18n.NewAgeNow)
	}
	if age < time.Hour {
		return copy.Count(i18n.CountMinute, int(age.Minutes()))
	}
	if age < 24*time.Hour {
		return copy.Count(i18n.CountHour, int(age.Hours()))
	}
	return copy.Count(i18n.CountDay, int(age.Hours()/24))
}
