package telegramcontroller_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

func TestArchiveSurfaceIsNewestFirstPagedAndNumbered(t *testing.T) {
	const node = domain.ComputerID("local")
	sessions := make([]domain.Session, 0, 9)
	for index := 1; index <= 8; index++ {
		id := fmt.Sprintf("%08d-1111-4111-9111-111111111111", index)
		session := archivedSession(t, id, domain.ProviderCodex, fmt.Sprintf("/work/session-%d", index), "provider", 1)
		snapshot := session.Snapshot()
		snapshot.ComputerID = node
		snapshot.Name = fmt.Sprintf("name %d", index)
		snapshot.StateChangedAt = session.CreatedAt().Add(time.Duration(index) * time.Hour)
		var err error
		session, err = domain.RestoreSession(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
	}
	remote := sessions[0].Snapshot()
	remote.ID = "99999999-1111-4111-9111-111111111111"
	remote.ComputerID = "remote"
	remote.Name = "remote"
	remoteSession, err := domain.RestoreSession(remote)
	if err != nil {
		t.Fatal(err)
	}
	sessions = append(sessions, remoteSession)

	controller := newController(t, nil, newLockedSessions(sessions...), nil, nil, telegramcontroller.Options{})
	t.Cleanup(func() { _ = controller.Close(context.Background()) })

	first, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuArchive})
	if err != nil {
		t.Fatal(err)
	}
	if first.Surface == nil || !first.Surface.RichMarkdown || strings.Contains(first.Surface.Text, "remote") {
		t.Fatalf("first archive surface = %#v", first.Surface)
	}
	wantTable := "Архив\n\n\u00a0\n\n| Name | Description |\n|---|---|\n| 1. name 8 |  |\n| 2. name 7 |  |"
	if !strings.Contains(first.Surface.Text, wantTable) || strings.Contains(first.Surface.Text, "name 2") {
		t.Fatalf("first archive text = %q", first.Surface.Text)
	}
	if got := archiveLabels(first.Surface.Rows); strings.Join(got, ",") != "1. name 8,2. name 7,3. name 6,4. name 5,5. name 4,6. name 3,◀,1/2,▶,Меню" {
		t.Fatalf("first archive labels = %#v", got)
	}
	if len(first.Surface.Rows) != 5 || len(first.Surface.Rows[0]) != 2 || len(first.Surface.Rows[1]) != 2 || len(first.Surface.Rows[2]) != 2 {
		t.Fatalf("first archive rows = %#v", first.Surface.Rows)
	}
	if first.Surface.Rows[3][0].Choice != 1 || first.Surface.Rows[3][1].Choice != 1 || first.Surface.Rows[3][2].Choice != 2 {
		t.Fatalf("first pager targets = %#v", first.Surface.Rows[3])
	}

	second, err := controller.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuArchive, Choice: 99})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second.Surface.Text, "| 7. name 2 |  |") || !strings.Contains(second.Surface.Text, "| 8. name 1 |  |") {
		t.Fatalf("clamped archive text = %q", second.Surface.Text)
	}
	if got := archiveLabels(second.Surface.Rows); strings.Join(got, ",") != "7. name 2,8. name 1,◀,2/2,▶,Меню" {
		t.Fatalf("second archive labels = %#v", got)
	}
	if second.Surface.Rows[1][0].Choice != 1 || second.Surface.Rows[1][1].Choice != 1 || second.Surface.Rows[1][2].Choice != 2 {
		t.Fatalf("second pager targets = %#v", second.Surface.Rows[1])
	}
}

type archivePromptState struct {
	reads   []domain.SessionID
	entries map[domain.SessionID][]string
	err     error
}

func (*archivePromptState) SetActiveSession(context.Context, domain.SessionID) error { return nil }
func (s *archivePromptState) LoadCardUserPrompts(_ context.Context, id domain.SessionID, limit int) ([]string, error) {
	s.reads = append(s.reads, id)
	if s.err != nil {
		return nil, s.err
	}
	entries := s.entries[id]
	return entries[:min(limit, len(entries))], nil
}

func TestArchiveDescriptionsUseOnlyVisibleKeyedPromptsAndSafeUnicodePreview(t *testing.T) {
	state := &archivePromptState{entries: make(map[domain.SessionID][]string)}
	var sessions []domain.Session
	for i := 0; i < 7; i++ {
		id := fmt.Sprintf("%08d-1111-4111-9111-111111111111", i+1)
		session := archivedSession(t, id, domain.ProviderCodex, "/never-context", "provider", 1)
		snapshot := session.Snapshot()
		snapshot.StateChangedAt = session.CreatedAt().Add(time.Duration(i) * time.Hour)
		var err error
		session, err = domain.RestoreSession(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		sessions = append(sessions, session)
	}
	state.entries[sessions[6].ID()] = []string{"🙋‍♂ caption | <b> `x`", "👨‍💻 processed\ntext", "❌ Ошибка препроцессинга\n🙅‍♂ original voice text", "👨‍💻 fourth excluded"}
	state.entries[sessions[5].ID()] = []string{"👨‍💻 " + strings.Repeat("Ж", 299), "🙋‍♂ 😀яя", "👨‍💻 excluded tail"}
	c := newController(t, nil, newLockedSessions(sessions...), nil, nil, telegramcontroller.Options{UIState: state})
	defer c.Close(context.Background())
	r, err := c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuArchive})
	if err != nil || r.Surface == nil {
		t.Fatalf("archive: %+v %v", r, err)
	}
	text := r.Surface.Text
	if !strings.Contains(text, "· caption \\| &lt;b&gt; \\`x\\`<br>· processed text") || strings.Contains(text, "original voice text") {
		t.Fatalf("prompt preview lost representation/escaping: %q", text)
	}
	if !strings.Contains(text, "· "+strings.Repeat("Ж", 60)) || strings.Contains(text, strings.Repeat("Ж", 61)) || !utf8.ValidString(text) {
		t.Fatalf("Unicode 60-rune per-prompt budget broken: %q", text)
	}
	for _, forbidden := range []string{"/never-context", "Ошибка препроцессинга", "fourth excluded", "excluded tail"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("archive includes forbidden %q: %q", forbidden, text)
		}
	}
	if len(state.reads) != 6 {
		t.Fatalf("read %d histories instead of visible6", len(state.reads))
	}
	for _, id := range state.reads {
		if id == sessions[0].ID() {
			t.Fatal("read off-page history")
		}
	}
	state.err = errors.New("private storage failure")
	r, err = c.HandleSemanticAction(context.Background(), telegramcontroller.SemanticAction{Kind: telegramcontroller.SemanticMenuArchive})
	if err != nil || r.Surface == nil || strings.Contains(r.Surface.Text, "caption") || strings.Contains(r.Surface.Text, "private storage") || strings.Contains(r.Surface.Text, "/never-context") {
		t.Fatalf("failed history read must leave description empty: %+v %v", r, err)
	}
}

func archiveLabels(rows [][]telegramcontroller.SemanticButton) []string {
	var labels []string
	for _, row := range rows {
		for _, button := range row {
			labels = append(labels, button.Label)
		}
	}
	return labels
}
