package telegramcontroller_test

import (
	"context"
	"os"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/sessionruntime"
	"bria/internal/telegramcontroller"
)

type formattingNative struct {
	snapshot sessionruntime.NativeSnapshot
}

func (n *formattingNative) NativeControl(_ context.Context, _ domain.SessionID, _ sessionruntime.NativeRequest) (sessionruntime.NativeSnapshot, error) {
	return n.snapshot, nil
}

func TestNativeCommandApprovalUsesRichLiteralPresentation(t *testing.T) {
	body, err := os.ReadFile("../nativeapproval/testdata/codex-command-approval-tracker-description.txt")
	if err != nil {
		t.Fatal(err)
	}
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &formattingNative{snapshot: sessionruntime.NativeSnapshot{
		Text: "plain fallback", FullText: string(body), Hash: strings.Repeat("a", 64), Interactive: true,
	}}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(context.Background())

	result, err := c.HandleSemanticMessage(context.Background(), message(1200, "/approval"))
	if err != nil || result.Surface == nil {
		t.Fatalf("approval surface = %#v, error = %v", result, err)
	}
	if !result.Surface.RichMarkdown || !strings.Contains(result.Surface.Text, "**Command**\n\n```shell\n") ||
		!strings.Contains(result.Surface.Text, `d=re.sub(r"[A-Za-z0-9._%+-]+@`) {
		t.Fatalf("approval surface is not safe Rich Markdown: %#v", result.Surface)
	}
	if result.Surface.NativeSessionID != ready.ID() || len(result.Surface.Rows) != 4 {
		t.Fatalf("approval controls changed: %#v", result.Surface)
	}
}

func TestGenericNativePickerKeepsPlainContentAndControls(t *testing.T) {
	const picker = "Select model and effort\n› 1. gpt-5.6_luna\n  2. codex-mini\nPress enter to confirm or esc to go back"
	ready := readySession(t, "11111111-1111-4111-9111-111111111111", domain.ProviderCodex, "/work", "provider-model", 1)
	native := &formattingNative{snapshot: sessionruntime.NativeSnapshot{
		Text: picker, FullText: picker, Hash: strings.Repeat("b", 64), Interactive: true,
	}}
	c := newController(t, nil, newLockedSessions(ready), nil, nil, telegramcontroller.Options{Recovered: []domain.Session{ready}, Native: native})
	defer c.Close(context.Background())

	result, err := c.HandleSemanticMessage(context.Background(), message(1201, "/model"))
	if err != nil || result.Surface == nil {
		t.Fatalf("generic picker surface = %#v, error = %v", result, err)
	}
	if result.Surface.Text != picker || result.Surface.RichMarkdown || result.Surface.NativeSessionID != ready.ID() || len(result.Surface.Rows) != 4 {
		t.Fatalf("generic picker behavior changed: %#v", result.Surface)
	}
}
