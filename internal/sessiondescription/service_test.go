package sessiondescription

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/transcript"
)

type stateStub struct{ state *domain.State }

func (s stateStub) State() *domain.State { return s.state.Clone() }

type promptStub struct {
	prompts       []string
	err           error
	limit         int
	discovery     transcript.Discovery
	discoverErr   error
	discoverCalls int
	request       transcript.Request
}

func (s *promptStub) DiscoverFresh(
	_ context.Context,
	_ transcript.Backend,
	_ string,
	_, _ int,
) (transcript.Discovery, error) {
	s.discoverCalls++
	return s.discovery, s.discoverErr
}

func (s *promptStub) Discover(
	ctx context.Context,
	backend transcript.Backend,
	workdir string,
	offset, limit int,
) (transcript.Discovery, error) {
	return s.DiscoverFresh(ctx, backend, workdir, offset, limit)
}

func (s *promptStub) ReadFirstUserTexts(
	_ context.Context,
	request transcript.Request,
	limit int,
) ([]string, error) {
	s.limit = limit
	s.request = request
	return append([]string(nil), s.prompts...), s.err
}

type archiveStub struct {
	prompts []string
	events  []transcript.Event
	calls   int
}

func (s *archiveStub) ReadArchivedInitialUserPrompts(
	context.Context,
	domain.Session,
) ([]string, error) {
	return append([]string(nil), s.prompts...), nil
}

func (s *archiveStub) ReadArchivedTranscript(
	context.Context,
	domain.Session,
) ([]transcript.Event, error) {
	s.calls++
	return append([]transcript.Event(nil), s.events...), nil
}

type generatorStub struct {
	backend string
	prompts []string
}

func (s *generatorStub) Generate(
	_ context.Context,
	backend string,
	prompts []string,
) ([]string, error) {
	s.backend = backend
	s.prompts = append([]string(nil), prompts...)
	return []string{"Контекст.", "Результат."}, nil
}

func TestLocalServiceUsesFirstThreeProviderPromptsWithoutReplicatingThem(t *testing.T) {
	session := descriptionSession()
	state := domain.NewState()
	state.Sessions[session.Ref().Key()] = session
	prompts := &promptStub{prompts: []string{"one", "two", "three"}}
	archives := &archiveStub{}
	generator := &generatorStub{}
	service, err := NewLocalService("node", stateStub{state}, prompts, archives, generator)
	if err != nil {
		t.Fatal(err)
	}
	result, err := service.Generate(context.Background(), requestFor(session))
	if err != nil {
		t.Fatal(err)
	}
	if prompts.limit != 3 || archives.calls != 0 || generator.backend != "codex" ||
		len(generator.prompts) != 3 || len(result.Lines) != 2 {
		t.Fatalf("limit=%d archive_calls=%d backend=%q prompts=%q result=%q",
			prompts.limit, archives.calls, generator.backend, generator.prompts, result.Lines)
	}
}

func TestLocalServiceFallsBackToEarliestArchivedUserPrompts(t *testing.T) {
	session := descriptionSession()
	state := domain.NewState()
	state.Sessions[session.Ref().Key()] = session
	prompts := &promptStub{err: transcript.ErrTranscriptNotFound}
	archives := &archiveStub{events: []transcript.Event{
		{Kind: transcript.EventAssistantText, Text: "skip"},
		{Kind: transcript.EventUserText, Text: "first"},
		{Kind: transcript.EventUserText, Text: "second"},
	}}
	generator := &generatorStub{}
	service, _ := NewLocalService("node", stateStub{state}, prompts, archives, generator)
	if _, err := service.Generate(context.Background(), requestFor(session)); err != nil {
		t.Fatal(err)
	}
	if archives.calls != 1 || len(generator.prompts) != 2 || generator.prompts[0] != "first" {
		t.Fatalf("archive_calls=%d prompts=%q", archives.calls, generator.prompts)
	}
}

func TestLocalServiceDescribesUnreadyAutomaticArchiveFromProviderTranscript(t *testing.T) {
	session := descriptionSession()
	session.ArchiveID = ""
	session.ArchiveReady = false
	state := domain.NewState()
	state.Sessions[session.Ref().Key()] = session
	prompts := &promptStub{prompts: []string{"first provider prompt"}}
	archives := &archiveStub{}
	generator := &generatorStub{}
	service, _ := NewLocalService("node", stateStub{state}, prompts, archives, generator)
	if _, err := service.Generate(context.Background(), requestFor(session)); err != nil {
		t.Fatal(err)
	}
	if archives.calls != 0 || len(generator.prompts) != 1 {
		t.Fatalf("archive_calls=%d prompts=%q", archives.calls, generator.prompts)
	}
}

func TestLocalServiceRecoversUniqueLegacyProviderByCreationTime(t *testing.T) {
	session := descriptionSession()
	session.ProviderSessionID = ""
	session.CreatedAt = time.Unix(100, 0).UTC()
	state := domain.NewState()
	state.Sessions[session.Ref().Key()] = session
	prompts := &promptStub{
		prompts: []string{"real first prompt"},
		discovery: transcript.Discovery{Total: 1, Candidates: []transcript.Candidate{{
			ProviderSessionID: "recovered-provider",
			CreatedAt:         session.CreatedAt.Add(300 * time.Millisecond),
		}}},
	}
	generator := &generatorStub{}
	service, _ := NewLocalService("node", stateStub{state}, prompts, &archiveStub{}, generator)

	result, err := service.Generate(context.Background(), requestFor(session))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Lines) != 2 || prompts.discoverCalls != 1 ||
		prompts.request.ProviderSessionID != "recovered-provider" ||
		len(generator.prompts) != 1 || generator.prompts[0] != "real first prompt" {
		t.Fatalf("result=%#v discover_calls=%d request=%#v prompts=%q",
			result, prompts.discoverCalls, prompts.request, generator.prompts)
	}
}

func TestLocalServiceClassifiesOnlyProvenLegacyEmptyArchive(t *testing.T) {
	session := descriptionSession()
	session.ProviderSessionID = ""
	state := domain.NewState()
	state.Sessions[session.Ref().Key()] = session
	prompts := &promptStub{err: transcript.ErrTranscriptNotFound}
	archives := &archiveStub{}
	generator := &generatorStub{}
	service, _ := NewLocalService("node", stateStub{state}, prompts, archives, generator)

	result, err := service.Generate(context.Background(), requestFor(session))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Empty || len(result.Lines) != 0 || archives.calls != 1 || len(generator.prompts) != 0 {
		t.Fatalf("result=%#v archive_calls=%d generated_prompts=%q",
			result, archives.calls, generator.prompts)
	}

	session.Name = "Known work"
	state.Sessions[session.Ref().Key()] = session
	if _, err := service.Generate(context.Background(), requestFor(session)); !errors.Is(err, transcript.ErrTranscriptNotFound) {
		t.Fatalf("named source-less archive error=%v", err)
	}
}

func TestLocalServiceRejectsStaleOrAlreadyDescribedSessionBeforeReading(t *testing.T) {
	for _, mutate := range []func(*domain.Session){
		func(session *domain.Session) { session.Revision++ },
		func(session *domain.Session) {
			session.DescriptionVersion = domain.ArchiveDescriptionVersion
			session.ArchiveDescription = []string{"Existing context.", "Existing result."}
		},
	} {
		session := descriptionSession()
		request := requestFor(session)
		mutate(&session)
		state := domain.NewState()
		state.Sessions[session.Ref().Key()] = session
		prompts := &promptStub{prompts: []string{"must not read"}}
		service, _ := NewLocalService(
			"node", stateStub{state}, prompts, &archiveStub{}, &generatorStub{},
		)
		if _, err := service.Generate(context.Background(), request); !errors.Is(err, domain.ErrStaleOperation) {
			t.Fatalf("error=%v", err)
		}
		if prompts.limit != 0 {
			t.Fatal("stale request read provider transcript")
		}
	}
}

func descriptionSession() domain.Session {
	return domain.Session{
		ID: "session", NodeID: "node", OwnerID: 7, Backend: "codex",
		ProviderSessionID: "provider", Workdir: "/work", State: domain.SessionArchived,
		RuntimePhase: domain.RuntimeIdle, RuntimeGeneration: 2, Revision: 4,
		ArchiveID: "archive", ArchiveReady: true, ArchivedAt: time.Unix(10, 0).UTC(),
	}
}

func requestFor(session domain.Session) Request {
	return Request{
		NodeID: session.NodeID, Session: session.Ref(), ArchiveID: session.ArchiveID,
		ExpectedRevision: session.Revision,
	}
}
