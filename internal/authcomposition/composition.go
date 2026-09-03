// Package authcomposition connects the provider-neutral Telegram authorization
// port to durable provider authenticators on one exact local computer.
package authcomposition

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"

	"bria/internal/authflow"
	"bria/internal/domain"
	"bria/internal/provider/claude"
	"bria/internal/provider/codex"
	"bria/internal/telegram"
	"bria/internal/telegramcontroller"
)

var ErrInvalidOptions = errors.New("authorization composition is unavailable")

// TelegramMessageDeleter is the exact Telegram primitive needed to remove an
// inbound secret after local provider processing. *telegram.Client implements
// it directly.
type TelegramMessageDeleter interface {
	DeleteMessage(context.Context, telegram.DeleteMessageRequest) error
}

// Options contains only local composition inputs. Empty provider executables
// disable that provider's Telegram authorization capability. ClaudeExecutable
// and ClaudeCredentialPath must either both be set or both be empty.
type Options struct {
	OwnerID              int64
	LocalComputerID      domain.ComputerID
	StorePath            string
	Telegram             TelegramMessageDeleter
	CodexExecutable      string
	ClaudeExecutable     string
	ClaudeCredentialPath string
}

// Flow is the concrete Telegram controller authorization port. It retains no
// submitted secret; authflow owns and destroys the required transient payload.
type Flow struct {
	localComputerID domain.ComputerID
	service         *authflow.Service
}

var _ telegramcontroller.AuthorizationFlow = (*Flow)(nil)

// Open validates and pins configured provider executables, opens the durable
// authorization replay fence, and returns the controller-facing flow.
func Open(options Options) (*Flow, error) {
	if !validOptions(options) {
		return nil, ErrInvalidOptions
	}
	store, err := authflow.OpenFileStore(options.StorePath)
	if err != nil {
		return nil, ErrInvalidOptions
	}
	authenticators := make(map[authflow.Provider]authflow.Authenticator, 2)
	if options.CodexExecutable != "" {
		authenticator, err := codex.NewCLIAuthenticator(options.CodexExecutable)
		if err != nil {
			return nil, ErrInvalidOptions
		}
		authenticators[authflow.ProviderCodex] = authenticator
	}
	if options.ClaudeExecutable != "" {
		authenticator, err := claude.NewAPIKeyAuthenticator(options.ClaudeExecutable, options.ClaudeCredentialPath)
		if err != nil {
			return nil, ErrInvalidOptions
		}
		authenticators[authflow.ProviderClaude] = authenticator
	}
	service, err := authflow.NewService(options.OwnerID, store, authenticators, telegramDeleter{client: options.Telegram})
	if err != nil {
		return nil, ErrInvalidOptions
	}
	return &Flow{localComputerID: options.LocalComputerID, service: service}, nil
}

func (flow *Flow) SupportsAuthorization(provider domain.Provider) bool {
	authProvider, ok := mappedProvider(provider)
	return flow != nil && ok && flow.service.Supports(authProvider)
}

func (flow *Flow) StartAuthorization(ctx context.Context, request telegramcontroller.AuthorizationStart) (telegramcontroller.AuthorizationChallenge, error) {
	if flow == nil || request.ComputerID != flow.localComputerID {
		return telegramcontroller.AuthorizationChallenge{}, authflow.ErrOperationConflict
	}
	provider, ok := mappedProvider(request.Provider)
	if !ok {
		return telegramcontroller.AuthorizationChallenge{}, authflow.ErrProviderUnavailable
	}
	result, err := flow.service.Start(ctx, authflow.StartRequest{
		OperationID: request.OperationID, ActorID: request.ActorID, ChatID: request.PrivateChatID,
		ConversationKind: request.ConversationKind, ComputerID: string(request.ComputerID), Provider: provider,
	})
	challenge := telegramcontroller.AuthorizationChallenge{
		OperationID: result.OperationID, ComputerID: request.ComputerID, Provider: request.Provider,
		ChallengeReference: result.ChallengeReference, Instruction: result.Instruction,
	}
	return challenge, err
}

func (flow *Flow) ConsumeAuthorizationMessage(ctx context.Context, request telegramcontroller.AuthorizationMessageLookup) (telegramcontroller.AuthorizationMessageBinding, error) {
	if flow == nil {
		return telegramcontroller.AuthorizationMessageBinding{}, authflow.ErrStateUnavailable
	}
	result, err := flow.service.ConsumeMessage(ctx, authflow.MessageRequest{
		ActorID: request.ActorID, ChatID: request.PrivateChatID,
		ConversationKind: request.ConversationKind, MessageID: request.SourceMessageID,
	})
	binding := telegramcontroller.AuthorizationMessageBinding{
		Bound: result.Bound, Authenticated: result.Status == authflow.StatusAuthenticated,
		DeletionKnown: result.Deletion == authflow.DeletionConfirmed,
	}
	if result.Provider != "" {
		provider, ok := mappedDomainProvider(result.Provider)
		if !ok {
			return telegramcontroller.AuthorizationMessageBinding{}, authflow.ErrStateUnavailable
		}
		binding.Provider = provider
	}
	return binding, err
}

func (flow *Flow) PendingAuthorizations(ctx context.Context, request telegramcontroller.AuthorizationPendingLookup) ([]telegramcontroller.PendingAuthorization, error) {
	if flow == nil {
		return nil, authflow.ErrStateUnavailable
	}
	operations, err := flow.service.Pending(ctx, authflow.PendingRequest{
		ActorID: request.ActorID, ChatID: request.PrivateChatID, ConversationKind: request.ConversationKind,
	})
	if err != nil {
		return nil, err
	}
	result := make([]telegramcontroller.PendingAuthorization, 0, len(operations))
	for _, operation := range operations {
		provider, ok := mappedDomainProvider(operation.Provider)
		if !ok || domain.ComputerID(operation.ComputerID) != flow.localComputerID || !flow.service.Supports(operation.Provider) {
			return nil, authflow.ErrStateUnavailable
		}
		result = append(result, telegramcontroller.PendingAuthorization{
			AuthorizationChallenge: telegramcontroller.AuthorizationChallenge{
				OperationID: operation.OperationID, ComputerID: flow.localComputerID, Provider: provider,
				ChallengeReference: operation.ChallengeReference,
			},
			AcceptsSecret: operation.Status == authflow.StatusAwaitingAction,
		})
	}
	return result, nil
}

func (flow *Flow) SubmitAuthorization(ctx context.Context, request telegramcontroller.AuthorizationSecret) (telegramcontroller.AuthorizationResult, error) {
	defer zeroBytes(request.Secret)
	if flow == nil {
		return telegramcontroller.AuthorizationResult{}, authflow.ErrStateUnavailable
	}
	if request.ComputerID != flow.localComputerID {
		result, discardErr := flow.service.Discard(ctx, authflow.DiscardRequest{
			OperationID: request.SubmissionOperationID, ActorID: request.ActorID, ChatID: request.PrivateChatID,
			ConversationKind: request.ConversationKind, MessageID: request.SourceMessageID,
		})
		return telegramcontroller.AuthorizationResult{DeletionKnown: result.Deletion == authflow.DeletionConfirmed},
			errors.Join(authflow.ErrOperationConflict, discardErr)
	}
	payload := authflow.NewSecretPayload(request.Secret)
	defer payload.Destroy()
	result, err := flow.service.Submit(ctx, authflow.SubmitRequest{
		OperationID: request.OperationID, SubmissionOperationID: request.SubmissionOperationID,
		ActorID: request.ActorID, ChatID: request.PrivateChatID, ConversationKind: request.ConversationKind,
		MessageID: request.SourceMessageID, ComputerID: string(request.ComputerID),
		Provider: authflow.Provider(request.Provider), ChallengeReference: request.ChallengeReference, Secret: payload,
	})
	return telegramcontroller.AuthorizationResult{
		Authenticated: result.Status == authflow.StatusAuthenticated,
		DeletionKnown: result.Deletion == authflow.DeletionConfirmed,
	}, err
}

func (flow *Flow) DiscardAuthorizationMessage(ctx context.Context, request telegramcontroller.AuthorizationDiscard) (telegramcontroller.AuthorizationResult, error) {
	if flow == nil {
		return telegramcontroller.AuthorizationResult{}, authflow.ErrStateUnavailable
	}
	result, err := flow.service.Discard(ctx, authflow.DiscardRequest{
		OperationID: request.OperationID, ActorID: request.ActorID, ChatID: request.PrivateChatID,
		ConversationKind: request.ConversationKind, MessageID: request.SourceMessageID,
	})
	return telegramcontroller.AuthorizationResult{DeletionKnown: result.Deletion == authflow.DeletionConfirmed}, err
}

type telegramDeleter struct {
	client TelegramMessageDeleter
}

func (deleter telegramDeleter) DeleteMessage(ctx context.Context, chatID, messageID int64) error {
	err := deleter.client.DeleteMessage(ctx, telegram.DeleteMessageRequest{
		ChatID: telegram.ChatID(chatID), MessageID: telegram.MessageID(messageID),
	})
	if telegramMessageAlreadyAbsent(err) {
		return nil
	}
	return err
}

func telegramMessageAlreadyAbsent(err error) bool {
	var apiError *telegram.APIError
	return errors.As(err, &apiError) && (apiError.HTTPStatus == 400 || apiError.ErrorCode == 400) &&
		strings.EqualFold(strings.TrimSpace(apiError.Description), "Bad Request: message to delete not found")
}

func mappedProvider(provider domain.Provider) (authflow.Provider, bool) {
	switch provider {
	case domain.ProviderCodex:
		return authflow.ProviderCodex, true
	case domain.ProviderClaude:
		return authflow.ProviderClaude, true
	default:
		return "", false
	}
}

func mappedDomainProvider(provider authflow.Provider) (domain.Provider, bool) {
	switch provider {
	case authflow.ProviderCodex:
		return domain.ProviderCodex, true
	case authflow.ProviderClaude:
		return domain.ProviderClaude, true
	default:
		return "", false
	}
}

func validOptions(options Options) bool {
	computerID := string(options.LocalComputerID)
	if options.OwnerID <= 0 || computerID == "" || strings.TrimSpace(computerID) != computerID ||
		strings.TrimSpace(options.StorePath) == "" || !filepath.IsAbs(options.StorePath) || nilInterface(options.Telegram) {
		return false
	}
	if options.CodexExecutable == "" && options.ClaudeExecutable == "" {
		return false
	}
	if (options.ClaudeExecutable == "") != (options.ClaudeCredentialPath == "") {
		return false
	}
	if options.ClaudeCredentialPath != "" {
		if !filepath.IsAbs(options.ClaudeCredentialPath) || filepath.Clean(options.StorePath) == filepath.Clean(options.ClaudeCredentialPath) {
			return false
		}
	}
	return true
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflection := reflect.ValueOf(value)
	switch reflection.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflection.IsNil()
	default:
		return false
	}
}

func zeroBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
