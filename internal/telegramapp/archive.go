package telegramapp

import (
	"context"
	"errors"
	"strconv"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/callbacktoken"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/i18n"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const maxArchiveCallbackPages = 512

func isArchiveContentAction(action telegramui.Action) bool {
	switch action {
	case telegramui.ActionSelectArchive, telegramui.ActionArchivePrevious,
		telegramui.ActionArchiveNext, telegramui.ActionArchiveBack,
		telegramui.ActionArchiveHistory, telegramui.ActionHistoryPrevious,
		telegramui.ActionHistoryNext:
		return true
	default:
		return false
	}
}

func (h *Handler) openArchivePage(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	refs, err := h.service.CallbackArchiveCandidates(actor)
	if err != nil {
		return telegramui.Screen{}, err
	}
	pages := max(1, (len(refs)+5)/6)
	values := make([]string, pages)
	for page := 1; page <= pages; page++ {
		values[page-1] = strconv.Itoa(page)
	}
	value, err := h.tokens.ResolveChoice(actor.UserID, action, "archive-page", token, values)
	if err != nil {
		return telegramui.Screen{}, err
	}
	page, err := strconv.Atoi(value)
	if err != nil {
		return telegramui.Screen{}, domain.ErrNotFound
	}
	return h.projector.OpenArchivesPage(actor, page)
}

func (h *Handler) openArchive(
	ctx context.Context,
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	target, err := h.resolveArchiveSelection(actor, telegramui.ActionSelectArchive, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return h.renderArchiveInspect(ctx, actor, target)
}

func (h *Handler) backToArchive(
	actor application.Principal,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	target, err := h.resolveArchiveSelection(actor, telegramui.ActionArchiveBack, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	return h.projector.OpenArchivesPage(actor, target.ListPage)
}

func (h *Handler) renderArchiveInspect(
	ctx context.Context,
	actor application.Principal,
	target callbacktoken.ArchiveSelection,
) (telegramui.Screen, error) {
	base, pages, err := h.archivePages(ctx, actor, target.Session)
	if err != nil {
		return telegramui.Screen{}, err
	}
	text := base.Text
	if pages.Latest.RichMarkdown != "" {
		if len(pages.Pages) > 1 {
			text += "\n\n" + h.copy(actor).Format(i18n.ArchiveOlderPages, len(pages.Pages)-1)
		}
		text += "\n\n" + pages.Latest.RichMarkdown
	}
	tokens := make(map[telegramui.Action]telegramui.OpaqueToken)
	for _, action := range []telegramui.Action{telegramui.ActionRestore} {
		tokens[action], err = h.tokens.Session(actor.UserID, action, target.Session)
		if err != nil {
			return telegramui.Screen{}, err
		}
	}
	tokens[telegramui.ActionArchiveBack], err = h.tokens.Archive(
		actor.UserID, telegramui.ActionArchiveBack, target.Session, target.ListPage,
	)
	if err != nil {
		return telegramui.Screen{}, err
	}
	if len(pages.Pages) > 0 {
		tokens[telegramui.ActionArchiveHistory], err = h.tokens.Page(
			actor.UserID, telegramui.ActionArchiveHistory, target.Session, len(pages.Pages),
		)
		if err != nil {
			return telegramui.Screen{}, err
		}
	}
	return telegramui.RenderArchiveInspect(telegramui.ArchiveInspectInput{
		Copy: h.copy(actor), Text: text, RichMarkdown: pages.Latest.RichMarkdown != "",
		CanRestore: screenHasAction(base, telegramui.ActionRestore),
		HasHistory: len(pages.Pages) > 0, Tokens: tokens,
	}), nil
}

func (h *Handler) openArchiveHistory(
	ctx context.Context,
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (telegramui.Screen, error) {
	target, err := h.resolveArchiveHistory(actor, action, token)
	if err != nil {
		return telegramui.Screen{}, err
	}
	base, pages, err := h.archivePages(ctx, actor, target.Session)
	if err != nil || len(pages.Pages) == 0 {
		return telegramui.Screen{}, errors.Join(err, domain.ErrNotFound)
	}
	page := min(max(1, target.Page), len(pages.Pages))
	content := pages.Pages[page-1].RichMarkdown
	previous, next, back, err := h.archiveHistoryTokens(actor, target.Session, page, len(pages.Pages))
	if err != nil {
		return telegramui.Screen{}, err
	}
	return telegramui.RenderArchiveHistory(telegramui.ArchiveHistoryInput{
		Copy: h.copy(actor), Text: base.Text + "\n\n" + content,
		RichMarkdown: content != "", Page: page, Pages: len(pages.Pages),
		PreviousToken: previous, NextToken: next, BackToken: back,
	}), nil
}

func (h *Handler) archivePages(
	ctx context.Context,
	actor application.Principal,
	ref domain.SessionRef,
) (telegramui.Screen, application.CardEventPages, error) {
	session, err := h.service.Session(actor, ref)
	if err != nil || session.State != domain.SessionArchived {
		return telegramui.Screen{}, application.CardEventPages{}, errors.Join(err, domain.ErrNotFound)
	}
	base, err := h.projector.SessionCard(actor, ref)
	if err != nil {
		return telegramui.Screen{}, application.CardEventPages{}, err
	}
	events := []application.CardEvent(nil)
	if h.controls.transcript != nil {
		if transcriptEvents, transcriptErr := h.controls.transcript.Transcript(ctx, actor, ref); transcriptErr == nil {
			events = cardEvents(transcriptEvents)
		}
	}
	preferences, err := h.service.Preferences(actor)
	if err != nil {
		return telegramui.Screen{}, application.CardEventPages{}, err
	}
	return base, application.RenderCardEventPages(
		preferences, events, application.CardRenderOptions{},
	), nil
}

func (h *Handler) resolveArchiveSelection(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (callbacktoken.ArchiveSelection, error) {
	refs, err := h.service.CallbackArchiveCandidates(actor)
	if err != nil {
		return callbacktoken.ArchiveSelection{}, err
	}
	pages := max(1, (len(refs)+5)/6)
	candidates := make([]callbacktoken.ArchiveSelection, 0, len(refs)*pages)
	for _, ref := range refs {
		for page := 1; page <= pages; page++ {
			candidates = append(candidates, callbacktoken.ArchiveSelection{
				Session: ref, ListPage: page,
			})
		}
	}
	target, err := h.tokens.ResolveArchive(actor.UserID, action, token, candidates)
	if err == nil || action != telegramui.ActionSelectArchive {
		return target, err
	}
	ref, legacyErr := h.tokens.ResolveSession(actor.UserID, action, token, refs)
	if legacyErr != nil {
		return callbacktoken.ArchiveSelection{}, legacyErr
	}
	page, pageErr := h.projector.ArchiveListPage(actor, ref)
	return callbacktoken.ArchiveSelection{Session: ref, ListPage: page}, pageErr
}

func (h *Handler) resolveArchiveHistory(
	actor application.Principal,
	action telegramui.Action,
	token telegramui.OpaqueToken,
) (callbacktoken.SessionPage, error) {
	refs, err := h.service.CallbackArchiveCandidates(actor)
	if err != nil {
		return callbacktoken.SessionPage{}, err
	}
	candidates := make([]callbacktoken.SessionPage, 0, len(refs)*maxArchiveCallbackPages)
	for _, ref := range refs {
		for page := 1; page <= maxArchiveCallbackPages; page++ {
			candidates = append(candidates, callbacktoken.SessionPage{Session: ref, Page: page})
		}
	}
	return h.tokens.ResolvePage(actor.UserID, action, token, candidates)
}

func (h *Handler) archiveHistoryTokens(
	actor application.Principal,
	ref domain.SessionRef,
	page, pages int,
) (telegramui.OpaqueToken, telegramui.OpaqueToken, telegramui.OpaqueToken, error) {
	var previous, next telegramui.OpaqueToken
	var err error
	if pages > 1 {
		previousPage := page - 1
		if previousPage < 1 {
			previousPage = pages
		}
		previous, err = h.tokens.Page(
			actor.UserID, telegramui.ActionHistoryPrevious, ref, previousPage,
		)
	}
	if err == nil && pages > 1 {
		nextPage := page + 1
		if nextPage > pages {
			nextPage = 1
		}
		next, err = h.tokens.Page(
			actor.UserID, telegramui.ActionHistoryNext, ref, nextPage,
		)
	}
	back, backErr := h.tokens.Session(actor.UserID, telegramui.ActionSelectArchive, ref)
	return previous, next, back, errors.Join(err, backErr)
}

func screenHasAction(screen telegramui.Screen, action telegramui.Action) bool {
	for _, row := range screen.Grid {
		for _, button := range row {
			if button.Callback.Action == action {
				return true
			}
		}
	}
	return false
}
