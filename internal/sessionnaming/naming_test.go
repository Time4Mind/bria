package sessionnaming_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/sessionnaming"
)

type processorFunc func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error)

func (function processorFunc) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	return function(ctx, request)
}

type nameStore struct {
	session        domain.Session
	loadFailures   int
	renameFailures int
}

func (store *nameStore) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	if store.loadFailures > 0 {
		store.loadFailures--
		return domain.Session{}, errors.New("temporary load failure")
	}
	if store.session.ID() != id {
		return domain.Session{}, errors.New("not found")
	}
	return store.session, nil
}
func (store *nameStore) RenameSession(_ context.Context, id domain.SessionID, name string, source domain.SessionNameSource) (domain.Session, error) {
	if store.renameFailures > 0 {
		store.renameFailures--
		return domain.Session{}, errors.New("temporary rename failure")
	}
	current, err := store.Load(context.Background(), id)
	if err != nil {
		return domain.Session{}, err
	}
	store.session, err = current.Rename(name, source)
	return store.session, err
}

func directoryNamedSession(t *testing.T, id, name string) domain.Session {
	t.Helper()
	starting, err := domain.NewStartingSession(domain.SessionID(id), "intent-1", "local", domain.ProviderCodex, "/workspace")
	if err != nil {
		t.Fatal(err)
	}
	starting, err = starting.Rename(name, domain.SessionNameDirectory)
	if err != nil {
		t.Fatal(err)
	}
	return starting
}

func TestCheapGeneratorUsesBoundedIsolatedPrompt(t *testing.T) {
	generator := sessionnaming.CheapGenerator{Processor: processorFunc(func(_ context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
		if request.Text != "почини кнопку" || !strings.Contains(request.Instruction, "A-Z") || !strings.Contains(request.Instruction, "латини") {
			t.Fatalf("request = %#v", request)
		}
		return promptpreprocess.Result{Text: "`Menu button`"}, nil
	})}
	name, err := generator.Generate(context.Background(), "local", "session-1", "message-1", " почини кнопку ")
	if err != nil || name != "Menu butto" {
		t.Fatalf("Generate() = (%q, %v)", name, err)
	}
}

func TestNormalizeRequiresLatinLettersAndBoundsLabel(t *testing.T) {
	if got := sessionnaming.Normalize("  Fix menu buttons  "); got != "Fix menu" {
		t.Fatalf("Normalize() = %q", got)
	}
	if got := sessionnaming.Normalize("Исправление меню"); got != "" {
		t.Fatalf("Normalize() accepted Cyrillic label %q", got)
	}
	if got := sessionnaming.Normalize("Кнопка/меню"); got != "" {
		t.Fatalf("Normalize() accepted punctuation in %q", got)
	}
	if got := sessionnaming.Normalize("Fast name Кириллица"); got != "" {
		t.Fatalf("Normalize() accepted trailing Cyrillic in %q", got)
	}
	if got := sessionnaming.Normalize("Fast name!"); got != "" {
		t.Fatalf("Normalize() accepted punctuation in %q", got)
	}
}

func TestCheapGeneratorRejectsCyrillicOutput(t *testing.T) {
	generator := sessionnaming.CheapGenerator{Processor: processorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
		return promptpreprocess.Result{Text: "Кнопка меню"}, nil
	})}
	if name, err := generator.Generate(context.Background(), "local", "session-1", "message-1", "почини кнопку"); err == nil || name != "" {
		t.Fatalf("Generate() = (%q, %v), want rejected Cyrillic output", name, err)
	}
}

func TestServiceIgnoresProviderTitleAndUsesModelFallback(t *testing.T) {
	for _, test := range []struct {
		provider string
		want     string
		source   domain.SessionNameSource
	}{
		{want: "Quick fix", source: domain.SessionNameModel},
		{provider: "CLI title", want: "Quick fix", source: domain.SessionNameModel},
	} {
		starting, err := domain.NewStartingSession("session-1", "intent-1", "local", domain.ProviderCodex, "/workspace")
		if err != nil {
			t.Fatal(err)
		}
		starting, err = starting.Rename("workspace", domain.SessionNameDirectory)
		if err != nil {
			t.Fatal(err)
		}
		store := &nameStore{session: starting}
		service, err := sessionnaming.New(store, func(context.Context) (bool, error) { return true, nil }, sessionnaming.CheapGenerator{Processor: processorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
			return promptpreprocess.Result{Text: "Quick fix"}, nil
		})})
		if err != nil {
			t.Fatal(err)
		}
		finish := service.Begin(context.Background(), starting, "message-1", "fix menu")
		finish(test.provider)
		if err := service.Wait(context.Background()); err != nil {
			t.Fatal(err)
		}
		if store.session.Name() != test.want || store.session.NameSource() != test.source {
			t.Fatalf("named session = %q/%q, want %q/%q", store.session.Name(), store.session.NameSource(), test.want, test.source)
		}
	}
}

func TestServiceRetriesNamingOnNextPromptAfterTransientFailure(t *testing.T) {
	starting := directoryNamedSession(t, "session-retry", "default")
	store := &nameStore{session: starting}
	attempts := 0
	service, err := sessionnaming.New(store, func(context.Context) (bool, error) { return true, nil }, sessionnaming.CheapGenerator{Processor: processorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
		attempts++
		if attempts == 1 {
			return promptpreprocess.Result{}, errors.New("temporary cheap-model failure")
		}
		return promptpreprocess.Result{Text: "Retry name"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	service.Begin(context.Background(), starting, "message-1", "first prompt")("ignored")
	if err := service.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.session.Name() != "default" || attempts != 1 {
		t.Fatalf("first attempt = name:%q attempts:%d", store.session.Name(), attempts)
	}
	service.Begin(context.Background(), store.session, "message-2", "second prompt")("ignored")
	if err := service.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.session.Name() != "Retry name" || store.session.NameSource() != domain.SessionNameModel || attempts != 2 {
		t.Fatalf("retry = name:%q source:%q attempts:%d", store.session.Name(), store.session.NameSource(), attempts)
	}
}

func TestServiceRetriesNamingAfterTransientPersistenceFailure(t *testing.T) {
	for _, test := range []struct {
		name  string
		store nameStore
	}{
		{name: "load", store: nameStore{session: directoryNamedSession(t, "session-load-retry", "default"), loadFailures: 1}},
		{name: "rename", store: nameStore{session: directoryNamedSession(t, "session-rename-retry", "default"), renameFailures: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &test.store
			service, err := sessionnaming.New(store, func(context.Context) (bool, error) { return true, nil }, sessionnaming.CheapGenerator{Processor: processorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
				return promptpreprocess.Result{Text: "Retry name"}, nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			service.Begin(context.Background(), store.session, "message-1", "first prompt")("")
			if err := service.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			service.Begin(context.Background(), store.session, "message-2", "second prompt")("")
			if err := service.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
			if store.session.Name() != "Retry name" || store.session.NameSource() != domain.SessionNameModel {
				t.Fatalf("retry result = %q/%q", store.session.Name(), store.session.NameSource())
			}
		})
	}
}

func TestServiceReportsSuccessfulLateRename(t *testing.T) {
	starting := directoryNamedSession(t, "session-refresh", "default")
	store := &nameStore{session: starting}
	release := make(chan struct{})
	refreshed := make(chan domain.SessionID, 1)
	service, err := sessionnaming.New(store, func(context.Context) (bool, error) { return true, nil }, sessionnaming.CheapGenerator{Processor: processorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
		<-release
		return promptpreprocess.Result{Text: "Late name"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	finish := service.BeginWithRefresh(context.Background(), starting, "message-1", "prompt", func(id domain.SessionID) { refreshed <- id })
	finish("")
	close(release)
	if err := service.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-refreshed:
		if id != starting.ID() {
			t.Fatalf("refreshed session = %s", id)
		}
	default:
		t.Fatal("successful late rename did not request refresh")
	}
}

func TestServiceAppliesGeneratedNameBeforePrimaryTurnFinishes(t *testing.T) {
	starting := directoryNamedSession(t, "session-early-refresh", "default")
	store := &nameStore{session: starting}
	refreshed := make(chan domain.SessionID, 1)
	service, err := sessionnaming.New(store, func(context.Context) (bool, error) { return true, nil }, sessionnaming.CheapGenerator{Processor: processorFunc(func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error) {
		return promptpreprocess.Result{Text: "Fast name"}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	finishPrimaryTurn := service.BeginWithRefresh(context.Background(), starting, "message-1", "prompt", func(id domain.SessionID) { refreshed <- id })
	if err := service.Wait(context.Background()); err != nil {
		t.Fatal(err)
	}
	if store.session.Name() != "Fast name" || store.session.NameSource() != domain.SessionNameModel {
		t.Fatalf("name before primary completion = %q/%q", store.session.Name(), store.session.NameSource())
	}
	select {
	case id := <-refreshed:
		if id != starting.ID() {
			t.Fatalf("refreshed session = %s", id)
		}
	default:
		t.Fatal("rename did not refresh before primary completion")
	}
	finishPrimaryTurn("")
}
