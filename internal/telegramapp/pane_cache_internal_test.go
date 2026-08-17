package telegramapp

import (
	"fmt"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/telegramui"
)

func TestPaneImageCacheIsBoundedAndCloned(t *testing.T) {
	state := newPaneRefreshState()
	for index := 0; index <= maxCachedPaneImages; index++ {
		state.rememberPaneImage(domain.SessionRef{
			NodeID: "node", SessionID: domain.SessionID(fmt.Sprintf("session-%d", index)),
		}, telegramui.PaneImage{PNG: []byte{byte(index)}, Hash: fmt.Sprintf("hash-%d", index)})
	}
	if _, ok := state.cachedPaneImage(domain.SessionRef{
		NodeID: "node", SessionID: "session-0",
	}, time.Minute); ok {
		t.Fatal("oldest pane image was not evicted")
	}
	latest := domain.SessionRef{NodeID: "node", SessionID: domain.SessionID(
		fmt.Sprintf("session-%d", maxCachedPaneImages),
	)}
	image, ok := state.cachedPaneImage(latest, time.Minute)
	if !ok {
		t.Fatal("latest pane image is missing")
	}
	image.PNG[0] = 0
	again, ok := state.cachedPaneImage(latest, time.Minute)
	if !ok || again.PNG[0] != byte(maxCachedPaneImages) {
		t.Fatalf("cached pane image was aliased: %#v", again.PNG)
	}
	state.paneMu.Lock()
	entry := state.paneImages[latest.Key()]
	entry.capturedAt = time.Now().Add(-time.Hour)
	state.paneImages[latest.Key()] = entry
	state.paneMu.Unlock()
	if _, ok := state.cachedPaneImage(latest, 0); !ok {
		t.Fatal("navigation cache unexpectedly expired")
	}
}

func TestPaneImageCachePromotesUploadToTelegramFileID(t *testing.T) {
	state := newPaneRefreshState()
	ref := domain.SessionRef{NodeID: "node", SessionID: "session"}
	state.rememberPaneImage(ref, telegramui.PaneImage{PNG: []byte("png"), Hash: "pane-v1"})
	state.rememberPaneFileID(ref, "stale-pane", "stale-file")
	before, ok := state.cachedPaneImage(ref, 0)
	if !ok || len(before.PNG) == 0 || before.FileID != "" {
		t.Fatalf("mismatched hash changed pane cache: %#v", before)
	}
	state.rememberPaneFileID(ref, "pane-v1", "telegram-photo")
	after, ok := state.cachedPaneImage(ref, 0)
	if !ok || len(after.PNG) != 0 || after.FileID != "telegram-photo" || after.Hash != "pane-v1" {
		t.Fatalf("pane cache was not promoted to file id: %#v", after)
	}
}
