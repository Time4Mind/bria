// Package settingscapability defines optional preference mutations independently
// of the base snapshot contract, so older compositions need not implement them.
package settingscapability

import "context"

// AutoApprovalPreferences controls automatic command approval.
type AutoApprovalPreferences interface {
	ToggleAutoApproveCommands(context.Context) error
}

// ScreenCapturePreferences controls the bounded native terminal capture.
type ScreenCapturePreferences interface {
	CycleScreenCaptureLimit(context.Context) error
}

// TechnicalOutputPreferences controls the post-wrap technical output budget.
type TechnicalOutputPreferences interface {
	CycleTechnicalOutputLines(context.Context) error
}

// TechnicalCommandPreferences independently controls the post-wrap command budget.
type TechnicalCommandPreferences interface {
	CycleTechnicalCommandLines(context.Context) error
}

// HiddenDirectoryPreferences controls dot-directory visibility in the browser.
type HiddenDirectoryPreferences interface {
	ToggleHiddenDirectories(context.Context) error
}
