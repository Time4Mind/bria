package main

import "testing"

func TestRefactoredLayersKeepOriginalForbiddenEdges(t *testing.T) {
	for _, edge := range [][2]string{
		{"internal/mediaproduction", "internal/telegram"},
		{"internal/mediaproduction", "internal/telegramcontroller"},
		{"internal/mediaproduction", "internal/documentproduction"},
		{"internal/nativeadapter", "internal/screen"},
		{"internal/nativecapture", "internal/nativerender"},
		{"internal/nativerender", "internal/screen"},
		{"internal/nativescreencache", "internal/screenproduction"},
		{"internal/settingscodec", "internal/settings"},
		{"internal/telegramrich", "internal/telegram"},
		{"internal/telegramhistory", "internal/storage"},
		{"internal/telegramcallbackview", "internal/telegrambridge"},
		{"internal/telegramturnhelpers", "internal/telegramcontroller"},
		{"internal/orphanresume", "internal/sessionruntime"},
		{"internal/orphanresume", "internal/app"},
	} {
		if failures := checkGraph(graphWithEdge(edge[0], edge[1])); len(failures) == 0 {
			t.Fatalf("refactor admits reverse dependency %s -> %s", edge[0], edge[1])
		}
	}
}
