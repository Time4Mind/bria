package nativecli

import (
	"os"
	"testing"
)

func TestBriaNativeParserExposesCommandApproval(t *testing.T) {
	data, err := os.ReadFile("../nativeapproval/testdata/codex-command-approval.txt")
	if err != nil {
		t.Fatal(err)
	}
	state := ParseScreen(string(data))
	if state.Ready || !state.Interactive || state.CommandApproval == nil || state.CommandApproval.OptionCount != 3 {
		t.Fatal("Bria native parser did not expose command approval", state)
	}
	closed := ParseScreen(string(data) + "\n› Ask Codex to do anything\ngpt-5.6-luna medium · /work")
	if closed.CommandApproval != nil {
		t.Fatal("historical approval exposed as actionable")
	}
	data, err = os.ReadFile("../nativeapproval/testdata/codex-command-approval-collapsed.txt")
	if err != nil {
		t.Fatal(err)
	}
	state = ParseScreen(string(data))
	if state.CommandApproval == nil || !state.CommandApproval.Incomplete || !state.ApprovalIncomplete || !state.Interactive {
		t.Fatal("incomplete approval lost its native state")
	}
}
