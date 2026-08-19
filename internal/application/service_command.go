package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"

	"github.com/Time4Mind/bria/internal/clusterstate"
)

func (s *Service) apply(
	ctx context.Context,
	kind clusterstate.CommandKind,
	payload any,
) error {
	scope, scoped := ctx.Value(operationScopeKey{}).(string)
	operationID := "scoped-pending"
	if !scoped || scope == "" {
		var err error
		operationID, err = s.newID()
		if err != nil {
			return err
		}
	}
	command, err := clusterstate.NewCommand(operationID, kind, s.now(), payload)
	if err != nil {
		return err
	}
	if scoped && scope != "" {
		identity, identityErr := stableCommandIdentity(payload, command.Payload)
		if identityErr != nil {
			return identityErr
		}
		hash := sha256.New()
		_, _ = hash.Write([]byte(scope))
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write([]byte(kind))
		_, _ = hash.Write([]byte{'\x00'})
		_, _ = hash.Write(identity)
		command.OperationID = "scoped-" + hex.EncodeToString(hash.Sum(nil)[:16])
	}
	result, err := s.applier.Apply(ctx, command)
	if err != nil {
		return err
	}
	return result.Err()
}

type operationScopeKey struct{}

// WithOperationScope makes command IDs deterministic inside one externally
// delivered event. Replaying the same Telegram update after leader failover
// therefore reuses the replicated operation ledger instead of duplicating a
// state transition.
func WithOperationScope(ctx context.Context, scope string) context.Context {
	if scope == "" {
		return ctx
	}
	return context.WithValue(ctx, operationScopeKey{}, scope)
}

// WithOperationSubscope separates multiple commands of the same kind emitted
// while handling one external event, without making unscoped background work
// deterministic across otherwise unrelated runs.
func WithOperationSubscope(ctx context.Context, subscope string) context.Context {
	scope, ok := ctx.Value(operationScopeKey{}).(string)
	if !ok || scope == "" || subscope == "" {
		return ctx
	}
	return WithOperationScope(ctx, scope+"\x00"+subscope)
}

func randomID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}
