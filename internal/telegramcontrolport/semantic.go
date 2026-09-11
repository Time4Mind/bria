package telegramcontrolport

import (
	"bria/internal/cardtranscript"
	"bria/internal/coordinator"
	"bria/internal/domain"
	"context"
)

type TechnicalHistoryStore interface {
	AppendCardTechnicalHistory(context.Context, domain.SessionID, string) error
}
type DisplayHistoryStore interface {
	LoadCardDisplayHistory(context.Context, domain.SessionID, bool) ([]string, error)
}
type TypedTranscriptStore interface {
	AppendCardTypedHistory(context.Context, domain.SessionID, string, string) error
	LoadCardTranscript(context.Context, domain.SessionID, bool) ([]cardtranscript.Block, error)
}
type TypedTranscriptSnapshotStore interface {
	LoadCardTranscriptSnapshot(context.Context, domain.SessionID, bool) (cardtranscript.Snapshot, error)
}
type TypedTranscriptInserter interface {
	InsertCardTypedHistoryAfterPrompt(context.Context, domain.SessionID, string, string, string) error
}

type SemanticActionKind string

type SemanticAction struct {
	Kind         SemanticActionKind
	SessionID    domain.SessionID
	Page         int
	FollowLatest bool
	SessionSlot  int
	Choice       int
	UpdateID     int64
}
type SemanticCarrierEffect string

const SemanticEditSameCarrier SemanticCarrierEffect = "edit_same_carrier"

type SemanticContentPage = cardtranscript.Page
type SemanticPageView struct {
	Page         int
	Pages        int
	Anchor       string
	FollowLatest bool
}
type SemanticCard struct {
	SessionID domain.SessionID
	Effect    SemanticCarrierEffect
	// OpenLatest marks a card created by a new user input. Its new carrier must
	// start at the current tail independently of the previous carrier's view.
	OpenLatest                         bool
	Header                             string
	Footer                             string
	Pages                              []SemanticContentPage
	View                               SemanticPageView
	Working, Archived, OptionsExpanded bool
	Recovery                           bool
	SelectableSessionIDs               []domain.SessionID
	SelectableSessionLabels            []string
	SessionRowSizes                    []int
	CloseConfirmation, MakeActive      bool
	DeleteConfirmation                 bool
}
type SemanticActionResult struct {
	Decision coordinator.Decision
	Card     *SemanticCard
	Surface  *SemanticSurface
}
type SemanticButton struct {
	Label     string
	Action    SemanticActionKind
	SessionID domain.SessionID
	Choice    int
}
type SemanticSurface struct {
	Text            string
	RichMarkdown    bool
	Rows            [][]SemanticButton
	NativeSessionID domain.SessionID
}
