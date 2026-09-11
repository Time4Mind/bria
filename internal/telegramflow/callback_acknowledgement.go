package telegramflow

import "bria/internal/telegramcallbackack"

const (
	CallbackAcknowledgementPending   = telegramcallbackack.Pending
	CallbackAcknowledgementConfirmed = telegramcallbackack.Confirmed
	CallbackAcknowledgementFailed    = telegramcallbackack.Failed
	CallbackAcknowledgementAbandoned = telegramcallbackack.Abandoned
)

type CallbackAcknowledgementStore = telegramcallbackack.Recorder
