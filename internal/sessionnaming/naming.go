// Package sessionnaming derives short Telegram labels without delaying the
// primary provider turn.
package sessionnaming

import (
	"context"
	"errors"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"

	"bria/internal/domain"
	"bria/internal/promptpreprocess"
)

const instruction = `Назови сессию по сути запроса. Верни только уникализируемое название: одно или два слова, не более 10 символов суммарно, без кавычек, пояснений и знаков препинания.`

type Generator interface {
	Generate(context.Context, domain.ComputerID, domain.SessionID, string, string) (string, error)
}

type Store interface {
	Load(context.Context, domain.SessionID) (domain.Session, error)
	RenameSession(context.Context, domain.SessionID, string, domain.SessionNameSource) (domain.Session, error)
}

type Service struct {
	store     Store
	enabled   func(context.Context) (bool, error)
	generator Generator
	mu        sync.Mutex
	attempted map[domain.SessionID]bool
	jobs      sync.WaitGroup
}

func New(store Store, enabled func(context.Context) (bool, error), generator Generator) (*Service, error) {
	if store == nil || enabled == nil || generator == nil {
		return nil, errors.New("session naming dependencies are required")
	}
	return &Service{store: store, enabled: enabled, generator: generator, attempted: make(map[domain.SessionID]bool)}, nil
}

// Begin starts the optional model fallback and returns a completion hook. The
// caller invokes the hook only after its lifecycle transition has committed.
func (service *Service) Begin(ctx context.Context, session domain.Session, messageID, text string) func(string) {
	return service.BeginWithRefresh(ctx, session, messageID, text, nil)
}

// BeginWithRefresh reports a committed rename so a visible card can refresh
// even when cheap-model generation finishes after the provider final.
func (service *Service) BeginWithRefresh(ctx context.Context, session domain.Session, messageID, text string, refreshed func(domain.SessionID)) func(string) {
	var generated <-chan string
	enabled, err := service.enabled(ctx)
	if err == nil && enabled && session.NameSource() == domain.SessionNameDirectory {
		service.mu.Lock()
		if !service.attempted[session.ID()] {
			service.attempted[session.ID()] = true
			result := make(chan string, 1)
			generated = result
			service.jobs.Add(1)
			go func() {
				defer service.jobs.Done()
				defer close(result)
				name, generateErr := service.generator.Generate(ctx, session.ComputerID(), session.ID(), messageID+":name", text)
				if generateErr != nil {
					// A transient cheap-model failure must not permanently pin the
					// directory fallback. The next user turn may retry; successful
					// naming remains naturally terminal through NameSourceModel.
					service.mu.Lock()
					delete(service.attempted, session.ID())
					service.mu.Unlock()
					return
				}
				result <- name
			}()
		}
		service.mu.Unlock()
	}
	return func(providerName string) {
		// Provider titles are CLI-internal metadata, never user-facing Bria
		// session names.  Naming is either the directory fallback or the
		// optional cheap-model result.
		_ = providerName
		if generated == nil {
			return
		}
		select {
		case name, ok := <-generated:
			if ok {
				if service.apply(ctx, session.ID(), name, domain.SessionNameModel) && refreshed != nil {
					refreshed(session.ID())
				}
			}
		default:
			service.jobs.Add(1)
			go func() {
				defer service.jobs.Done()
				if name, ok := <-generated; ok {
					if service.apply(ctx, session.ID(), name, domain.SessionNameModel) && refreshed != nil {
						refreshed(session.ID())
					}
				}
			}()
		}
	}
}

func (service *Service) apply(ctx context.Context, id domain.SessionID, name string, source domain.SessionNameSource) bool {
	current, err := service.store.Load(context.WithoutCancel(ctx), id)
	if err != nil {
		service.allowRetry(id)
		return false
	}
	if current.NameSource() == domain.SessionNameProvider || source == domain.SessionNameModel && current.NameSource() != domain.SessionNameDirectory {
		return false
	}
	if _, err := service.store.RenameSession(context.WithoutCancel(ctx), id, name, source); err != nil {
		service.allowRetry(id)
		return false
	}
	return true
}

func (service *Service) allowRetry(id domain.SessionID) {
	service.mu.Lock()
	delete(service.attempted, id)
	service.mu.Unlock()
}

func (service *Service) Wait(ctx context.Context) error {
	done := make(chan struct{})
	go func() { service.jobs.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type CheapGenerator struct{ Processor promptpreprocess.Processor }

func (generator CheapGenerator) Generate(ctx context.Context, computerID domain.ComputerID, sessionID domain.SessionID, messageID, text string) (string, error) {
	if generator.Processor == nil {
		return "", errors.New("session name processor is unavailable")
	}
	result, err := generator.Processor.Process(ctx, promptpreprocess.Request{
		ComputerID: computerID, SessionID: sessionID, MessageID: messageID,
		Instruction: instruction, Text: strings.TrimSpace(text),
	})
	if err != nil {
		return "", err
	}
	name := Normalize(result.Text)
	if name == "" {
		return "", errors.New("session name output is invalid")
	}
	return name, nil
}

// Normalize converts a provider or cheap-model title to Bria's stable
// one-or-two-word, ten-rune label. Empty means that no safe label was found.
func Normalize(value string) string {
	value = strings.Trim(strings.TrimSpace(value), "`'\"«»")
	words := strings.Fields(value)
	result := make([]string, 0, 2)
	runes := 0
	for _, word := range words {
		word = strings.TrimFunc(word, func(character rune) bool {
			return !unicode.IsLetter(character) && !unicode.IsDigit(character)
		})
		if word == "" {
			continue
		}
		// The user-facing automatic name is deliberately ASCII/English.  Do
		// not leak a provider's localized title into the card header.
		for _, character := range word {
			if !(character >= 'A' && character <= 'Z') && !(character >= 'a' && character <= 'z') && !(character >= '0' && character <= '9') {
				return ""
			}
		}
		remaining := domain.MaxSessionNameRunes - runes
		if len(result) > 0 {
			remaining--
		}
		if remaining <= 0 {
			break
		}
		wordRunes := []rune(word)
		if len(wordRunes) > remaining {
			wordRunes = wordRunes[:remaining]
		}
		result = append(result, string(wordRunes))
		runes += utf8.RuneCountInString(string(wordRunes))
		if len(result) == 2 || runes == domain.MaxSessionNameRunes {
			break
		}
	}
	return strings.Join(result, " ")
}
