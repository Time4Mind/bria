// Package callbacktoken creates compact, non-reversible Telegram callback
// references. Entity identifiers never need to appear in callback_data.
package callbacktoken

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

const (
	KeyBytes    = 32
	digestBytes = 16
)

type Codec struct {
	key []byte
}

type SessionPage struct {
	Session domain.SessionRef
	Page    int
}

type InteractiveSession struct {
	Session    domain.SessionRef
	PromptHash string
}

func New(key []byte) (*Codec, error) {
	if len(key) != KeyBytes {
		return nil, fmt.Errorf("callback key must be exactly %d bytes", KeyBytes)
	}
	return &Codec{key: append([]byte(nil), key...)}, nil
}

func (c *Codec) Node(
	actor domain.UserID,
	action telegramui.Action,
	nodeID domain.NodeID,
) (telegramui.OpaqueToken, error) {
	return c.token(actor, action, "node", string(nodeID))
}

func (c *Codec) Session(
	actor domain.UserID,
	action telegramui.Action,
	ref domain.SessionRef,
) (telegramui.OpaqueToken, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	return c.token(actor, action, "session", ref.Key())
}

func (c *Codec) Page(
	actor domain.UserID,
	action telegramui.Action,
	ref domain.SessionRef,
	page int,
) (telegramui.OpaqueToken, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if page < 1 {
		return "", errors.New("page must be positive")
	}
	return c.token(actor, action, "session_page", ref.Key()+"\x00"+strconv.Itoa(page))
}

func (c *Codec) Interactive(
	actor domain.UserID,
	action telegramui.Action,
	ref domain.SessionRef,
	promptHash string,
) (telegramui.OpaqueToken, error) {
	if err := ref.Validate(); err != nil {
		return "", err
	}
	if len(promptHash) != 32 {
		return "", errors.New("interactive prompt hash is invalid")
	}
	return c.token(actor, action, "interactive", ref.Key()+"\x00"+promptHash)
}

func (c *Codec) Choice(
	actor domain.UserID,
	action telegramui.Action,
	flowID string,
	value string,
) (telegramui.OpaqueToken, error) {
	return c.token(actor, action, "choice", flowID+"\x00"+value)
}

func (c *Codec) ResolveChoice(
	actor domain.UserID,
	action telegramui.Action,
	flowID string,
	token telegramui.OpaqueToken,
	candidates []string,
) (string, error) {
	match := ""
	for _, candidate := range candidates {
		expected, err := c.Choice(actor, action, flowID, candidate)
		if err != nil {
			return "", err
		}
		if tokenEqual(expected, token) {
			if match != "" {
				return "", errors.New("ambiguous callback token")
			}
			match = candidate
		}
	}
	if match == "" {
		return "", domain.ErrNotFound
	}
	return match, nil
}

func (c *Codec) ResolveNode(
	actor domain.UserID,
	action telegramui.Action,
	token telegramui.OpaqueToken,
	candidates []domain.NodeID,
) (domain.NodeID, error) {
	var match domain.NodeID
	for _, candidate := range candidates {
		expected, err := c.Node(actor, action, candidate)
		if err != nil {
			return "", err
		}
		if tokenEqual(expected, token) {
			if match != "" {
				return "", errors.New("ambiguous callback token")
			}
			match = candidate
		}
	}
	if match == "" {
		return "", domain.ErrNotFound
	}
	return match, nil
}

func (c *Codec) ResolveSession(
	actor domain.UserID,
	action telegramui.Action,
	token telegramui.OpaqueToken,
	candidates []domain.SessionRef,
) (domain.SessionRef, error) {
	var match domain.SessionRef
	found := false
	for _, candidate := range candidates {
		expected, err := c.Session(actor, action, candidate)
		if err != nil {
			return domain.SessionRef{}, err
		}
		if tokenEqual(expected, token) {
			if found {
				return domain.SessionRef{}, errors.New("ambiguous callback token")
			}
			match = candidate
			found = true
		}
	}
	if !found {
		return domain.SessionRef{}, domain.ErrNotFound
	}
	return match, nil
}

func (c *Codec) ResolvePage(
	actor domain.UserID,
	action telegramui.Action,
	token telegramui.OpaqueToken,
	candidates []SessionPage,
) (SessionPage, error) {
	var match SessionPage
	found := false
	for _, candidate := range candidates {
		expected, err := c.Page(actor, action, candidate.Session, candidate.Page)
		if err != nil {
			return SessionPage{}, err
		}
		if tokenEqual(expected, token) {
			if found {
				return SessionPage{}, errors.New("ambiguous callback token")
			}
			match = candidate
			found = true
		}
	}
	if !found {
		return SessionPage{}, domain.ErrNotFound
	}
	return match, nil
}

func (c *Codec) ResolveInteractive(
	actor domain.UserID,
	action telegramui.Action,
	token telegramui.OpaqueToken,
	candidates []InteractiveSession,
) (InteractiveSession, error) {
	var match InteractiveSession
	found := false
	for _, candidate := range candidates {
		expected, err := c.Interactive(actor, action, candidate.Session, candidate.PromptHash)
		if err != nil {
			return InteractiveSession{}, err
		}
		if tokenEqual(expected, token) {
			if found {
				return InteractiveSession{}, errors.New("ambiguous callback token")
			}
			match, found = candidate, true
		}
	}
	if !found {
		return InteractiveSession{}, domain.ErrNotFound
	}
	return match, nil
}

func (c *Codec) token(
	actor domain.UserID,
	action telegramui.Action,
	kind string,
	identifier string,
) (telegramui.OpaqueToken, error) {
	if c == nil || len(c.key) != KeyBytes {
		return "", errors.New("callback codec is not initialized")
	}
	if actor <= 0 || strings.TrimSpace(string(action)) == "" || strings.TrimSpace(identifier) == "" {
		return "", errors.New("actor, action, and identifier are required")
	}
	mac := hmac.New(sha256.New, c.key)
	_, _ = mac.Write([]byte(strconv.FormatInt(int64(actor), 10)))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(action))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(kind))
	_, _ = mac.Write([]byte{'\x00'})
	_, _ = mac.Write([]byte(identifier))
	digest := mac.Sum(nil)
	return telegramui.OpaqueToken(
		base64.RawURLEncoding.EncodeToString(digest[:digestBytes]),
	), nil
}

func tokenEqual(a, b telegramui.OpaqueToken) bool {
	left := []byte(a)
	right := []byte(b)
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare(left, right) == 1
}
