package sessionnaming_test

import (
	"context"
	"errors"
	"testing"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
	"bria/internal/sessionnaming"
)

type processorFunc func(context.Context, promptpreprocess.Request) (promptpreprocess.Result, error)

func (function processorFunc) Process(ctx context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
	return function(ctx, request)
}

type nameStore struct{ session domain.Session }

func (store *nameStore) Load(_ context.Context, id domain.SessionID) (domain.Session, error) {
	if store.session.ID() != id {
		return domain.Session{}, errors.New("not found")
	}
	return store.session, nil
}
func (store *nameStore) RenameSession(_ context.Context, id domain.SessionID, name string, source domain.SessionNameSource) (domain.Session, error) {
	current, err := store.Load(context.Background(), id)
	if err != nil {
		return domain.Session{}, err
	}
	store.session, err = current.Rename(name, source)
	return store.session, err
}

func TestCheapGeneratorUsesBoundedIsolatedPrompt(t *testing.T) {
	generator := sessionnaming.CheapGenerator{Processor: processorFunc(func(_ context.Context, request promptpreprocess.Request) (promptpreprocess.Result, error) {
		if request.Text != "почини кнопку" || request.Instruction == "" {
			t.Fatalf("request = %#v", request)
		}
		return promptpreprocess.Result{Text: "`Menu button`"}, nil
	})}
	name, err := generator.Generate(context.Background(), "local", "session-1", "message-1", " почини кнопку ")
	if err != nil || name != "Menu butto" {
		t.Fatalf("Generate() = (%q, %v)", name, err)
	}
}

func TestNormalizeRequiresShortEnglishLabel(t *testing.T) {
	if got := sessionnaming.Normalize("  Fix menu buttons  "); got != "Fix menu" {
		t.Fatalf("Normalize() = %q", got)
	}
	if got := sessionnaming.Normalize("Исправление меню"); got != "" {
		t.Fatalf("Normalize() accepted non-English label %q", got)
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
