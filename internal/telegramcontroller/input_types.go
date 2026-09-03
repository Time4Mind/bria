package telegramcontroller

import "bria/internal/turnprocessing"

type IncomingInput = turnprocessing.IncomingInput
type InputPreparer = turnprocessing.InputPreparer
type AttachmentRef = turnprocessing.AttachmentRef
type PreparedInput = turnprocessing.PreparedInput
type StructuredInputPreparer = turnprocessing.StructuredInputPreparer
type PreparedTurnSubmitter = turnprocessing.PreparedTurnSubmitter
type AttachmentReceipt = turnprocessing.AttachmentReceipt
type AttachmentCustody = turnprocessing.AttachmentCustody
type SessionInput = turnprocessing.SessionInput
type InputReceipt = turnprocessing.InputReceipt
type DurableLeasedInput = turnprocessing.DurableLeasedInput
type DurableInputAcceptance = turnprocessing.DurableInputAcceptance
type DurableInputCompletion = turnprocessing.DurableInputCompletion

const (
	DurableInputSucceeded = turnprocessing.DurableInputSucceeded
	DurableInputFailed    = turnprocessing.DurableInputFailed
)

type DurableInputCallbacks = turnprocessing.DurableInputCallbacks
type DurableInputProcessReceipt = turnprocessing.DurableInputProcessReceipt
type DurableInputCustody = turnprocessing.DurableInputCustody
type RuntimeEventObservation = turnprocessing.RuntimeEventObservation
type RuntimeEventObserver = turnprocessing.RuntimeEventObserver
type FinalObservation = turnprocessing.FinalObservation
type FinalProcessor = turnprocessing.FinalProcessor
