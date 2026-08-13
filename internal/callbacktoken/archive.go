package callbacktoken

import (
	"errors"
	"strconv"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

type ArchiveSelection struct {
	Session  domain.SessionRef
	ListPage int
}

func (c *Codec) Archive(
	actor domain.UserID,
	action telegramui.Action,
	ref domain.SessionRef,
	listPage int,
) (telegramui.OpaqueToken, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if listPage < 1 {
		return "", errors.New("archive list page must be positive")
	}
	return c.token(
		actor, action, "archive", ref.Key()+"\x00"+strconv.Itoa(listPage),
	)
}

func (c *Codec) ResolveArchive(
	actor domain.UserID,
	action telegramui.Action,
	token telegramui.OpaqueToken,
	candidates []ArchiveSelection,
) (ArchiveSelection, error) {
	var match ArchiveSelection
	found := false
	for _, candidate := range candidates {
		expected, err := c.Archive(actor, action, candidate.Session, candidate.ListPage)
		if err != nil {
			return ArchiveSelection{}, err
		}
		if !tokenEqual(expected, token) {
			continue
		}
		if found {
			return ArchiveSelection{}, errors.New("ambiguous callback token")
		}
		match, found = candidate, true
	}
	if !found {
		return ArchiveSelection{}, domain.ErrNotFound
	}
	return match, nil
}
