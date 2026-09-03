// Package statusrecovery defines transport-neutral identity for resolving one
// exact unknown Telegram status write.
package statusrecovery

import (
	"math"
	"strings"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/telegramstate"
	"bria/internal/telegramui"
)

type ScopeKind string

const (
	ScopeSession ScopeKind = "session"
	ScopeGlobal  ScopeKind = "global"
)

type Scope struct {
	Kind      ScopeKind        `json:"kind"`
	SessionID domain.SessionID `json:"session_id,omitempty"`
}

type Binding struct {
	OperationID string                `json:"operation_id"`
	UpdateID    int64                 `json:"update_id"`
	Scope       Scope                 `json:"scope"`
	Carrier     telegramstate.Carrier `json:"carrier"`
	Sequence    uint64                `json:"sequence"`
	Prepared    bool                  `json:"prepared"`
	Edit        bool                  `json:"edit"`
}

func Valid(binding Binding) bool {
	if strings.TrimSpace(binding.OperationID) == "" || len(binding.OperationID) > 256 || !utf8.ValidString(binding.OperationID) ||
		binding.UpdateID <= 0 || binding.Sequence == 0 || binding.Sequence > math.MaxInt64 || binding.UpdateID != int64(binding.Sequence) ||
		binding.Carrier.ChatID <= 0 || binding.Carrier.MessageID <= 0 {
		return false
	}
	switch binding.Scope.Kind {
	case ScopeSession:
		return binding.Scope.SessionID != "" && binding.Scope.SessionID != domain.SessionID(telegramui.GlobalSurfaceID)
	case ScopeGlobal:
		return binding.Scope.SessionID == ""
	default:
		return false
	}
}
