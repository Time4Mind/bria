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
}
