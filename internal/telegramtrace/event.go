// Package telegramtrace defines payload-free Telegram flow diagnostics.
package telegramtrace

import (
	"time"

	"bria/internal/controllertelemetry"
	"bria/internal/coordinator"
	"bria/internal/telegrampipeline"
	"bria/internal/telegramui"
)

// Identity fields are for in-memory correlation only. Record encodes them as
// per-process HMAC references before the safe logger sees an event.
type Event struct {
	Stage              string
	OperationID        string
	UpdateID           int64
	UpdateKind         coordinator.UpdateKind
	Action             telegramui.Action
	Effect             telegrampipeline.CallbackEffect
	Result             string
	Error              string
	Reason             string
	Duration           time.Duration
	Time               time.Time
	CallbackID         string
	SessionID          string
	ChatID             int64
	CarrierID          int64
	ExpectedChatID     int64
	ExpectedCarrierID  int64
	PresentationID     string
	ButtonIDs          []string
	Page               int
	Pages              int
	Target             int
	HasPage            bool
	FollowLatest       bool
	Retired            bool
	controllerEvent    controllertelemetry.Event
	hasControllerEvent bool
}
