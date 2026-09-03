// Package artifactcomposition routes only one correlated terminal observation
// into durable artifact delivery. It never derives artifacts from commentary.
package artifactcomposition

import (
	"context"
	"errors"
	"strings"

	"bria/internal/artifactproduction"
	"bria/internal/turnprocessing"
)

var ErrInvalidFinalObservation = errors.New("artifact composition final observation is invalid")

type Composition struct{ service *artifactproduction.Service }

func Open(service *artifactproduction.Service) (*Composition, error) {
	if service == nil {
		return nil, ErrInvalidFinalObservation
	}
	return &Composition{service: service}, nil
}

// ProcessFinal receives the controller's terminal-only callback. OperationID
// is the durable correlation key; Text is forwarded verbatim to the bounded
// artifact parser, with no history or commentary lookup.
func (composition *Composition) ProcessFinal(ctx context.Context, final turnprocessing.FinalObservation) error {
	_, err := composition.DeliverFinal(ctx, final)
	return err
}

// DeliverFinal exposes the result, including the signed retry descriptor, to
// the outer callback route while preserving FinalProcessor compatibility.
func (composition *Composition) DeliverFinal(ctx context.Context, final turnprocessing.FinalObservation) (artifactproduction.Result, error) {
	if composition == nil || composition.service == nil || ctx == nil || !validFinal(final) {
		return artifactproduction.Result{}, ErrInvalidFinalObservation
	}
	return composition.service.DeliverFinal(ctx, final.OperationID, final.Text)
}

// Retry is the explicit route for an artifactproduction signed descriptor.
// The token is not parsed or widened by composition.
func (composition *Composition) Retry(ctx context.Context, descriptor string) (artifactproduction.Result, error) {
	if composition == nil || composition.service == nil || ctx == nil || strings.TrimSpace(descriptor) != descriptor || descriptor == "" {
		return artifactproduction.Result{}, ErrInvalidFinalObservation
	}
	return composition.service.Retry(ctx, descriptor)
}

func (composition *Composition) RecoverClaimedResults(ctx context.Context) ([]artifactproduction.RecoveredRetry, error) {
	if composition == nil || composition.service == nil || ctx == nil {
		return nil, ErrInvalidFinalObservation
	}
	return composition.service.RecoverClaimedResults(ctx)
}

func validFinal(final turnprocessing.FinalObservation) bool {
	return final.SessionID != "" && strings.TrimSpace(final.MessageID) != "" &&
		final.OperationID == final.MessageID+":final"
}

var _ turnprocessing.FinalProcessor = (*Composition)(nil)
