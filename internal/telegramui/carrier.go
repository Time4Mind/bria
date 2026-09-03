package telegramui

// CarrierEffect describes the single Telegram carrier operation requested by
// a projection. It deliberately contains no chat or message identifiers.
type CarrierEffect string

const (
	EffectEditSameCarrier             CarrierEffect = "edit_same_carrier"
	EffectSendOneNewCard              CarrierEffect = "send_one_new_card"
	EffectSendOneBackgroundCompletion CarrierEffect = "send_one_background_completion"
)

// CardProjectionInput contains current semantic content and keyboard context.
// Keyboard.View is derived from View and ignored.
type CardProjectionInput struct {
	Pages    []ContentPage
	View     PageView
	Keyboard CardKeyboardInput
}

type ProjectedCard struct {
	Pages    []ContentPage
	View     PageView
	Keyboard CardKeyboard
}

type CarrierProjection struct {
	Effect                CarrierEffect
	PreviousCardUnchanged bool
	Card                  ProjectedCard
	Notification          *BackgroundCompletionNotification
}

// BackgroundCompletionNotification is deliberately content-free. Count is
// always one and Action always selects the completed session, preventing a
// background final from being projected into chat before the user opens it.
type BackgroundCompletionNotification struct {
	Count         int
	Action        Action
	ContainsFinal bool
}

// ProjectPageNavigation requests an edit of the existing carrier after
// resolving the page action against current content.
func ProjectPageNavigation(input CardProjectionInput, action Action) (CarrierProjection, error) {
	view, err := NavigatePage(input.View, action, input.Pages)
	if err != nil {
		return CarrierProjection{}, err
	}
	card, err := projectCard(input, view)
	if err != nil {
		return CarrierProjection{}, err
	}
	return CarrierProjection{Effect: EffectEditSameCarrier, Card: card}, nil
}

// ProjectActiveFinal requests exactly one new carrier, opens its latest page,
// and explicitly retains the previously pinned card without editing it.
func ProjectActiveFinal(input CardProjectionInput) (CarrierProjection, error) {
	view, err := NavigatePage(input.View, ActionPageLatest, input.Pages)
	if err != nil {
		return CarrierProjection{}, err
	}
	card, err := projectCard(input, view)
	if err != nil {
		return CarrierProjection{}, err
	}
	return CarrierProjection{
		Effect:                EffectSendOneNewCard,
		PreviousCardUnchanged: true,
		Card:                  card,
	}, nil
}

// ProjectCompletion produces the mutually exclusive product behavior for a
// final: an active session gets one new card, while a background session keeps
// the completed card for later selection and emits one compact notification.
func ProjectCompletion(input CardProjectionInput, active bool) (CarrierProjection, error) {
	if active {
		return ProjectActiveFinal(input)
	}
	view, err := NavigatePage(input.View, ActionPageLatest, input.Pages)
	if err != nil {
		return CarrierProjection{}, err
	}
	card, err := projectCard(input, view)
	if err != nil {
		return CarrierProjection{}, err
	}
	return CarrierProjection{
		Effect:                EffectSendOneBackgroundCompletion,
		PreviousCardUnchanged: true,
		Card:                  card,
		Notification: &BackgroundCompletionNotification{
			Count:  1,
			Action: ActionSelectSession,
		},
	}, nil
}

func projectCard(input CardProjectionInput, view PageView) (ProjectedCard, error) {
	keyboardInput := input.Keyboard
	keyboardInput.View = view
	keyboard, err := ProjectCardKeyboard(keyboardInput)
	if err != nil {
		return ProjectedCard{}, err
	}
	return ProjectedCard{
		Pages:    cloneContentPages(input.Pages),
		View:     view,
		Keyboard: keyboard,
	}, nil
}

func cloneContentPages(pages []ContentPage) []ContentPage {
	cloned := make([]ContentPage, len(pages))
	for index, page := range pages {
		cloned[index] = page
		cloned[index].Anchors = append([]string(nil), page.Anchors...)
	}
	return cloned
}
