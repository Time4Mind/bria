// Package preprocessinglog records payload-free preprocessing diagnostics.
package preprocessinglog

import (
	"context"
	"errors"
	"strconv"

	"bria/internal/observability"
	"bria/internal/promptpreprocess"
	"bria/internal/promptpreprocesssession"
	"bria/internal/safelog"
)

type Observer struct {
	Logger *safelog.Logger
	Inputs observability.InputReferencer
}

type SessionObserver struct {
	Logger *safelog.Logger
	Inputs observability.InputReferencer
}

func (observer SessionObserver) ObservePreprocessingSession(_ context.Context, observation promptpreprocesssession.LifecycleObservation) error {
	if observer.Logger == nil {
		return errors.New("safe logger is required")
	}
	fields := map[string]string{
		"state": observation.State, "provider": string(observation.Provider), "model": observation.Model,
		"duration_ms": strconv.FormatInt(max(0, observation.Duration.Milliseconds()), 10),
	}
	addInputRef(fields, observer.Inputs, observation.MessageID)
	return observer.Logger.Write(safelog.Event{Class: safelog.Service, Type: "prompt.preprocessing_session",
		ErrorCategory: observation.ErrorCategory, Fields: fields})
}

func (observer Observer) ObservePreprocessing(_ context.Context, observation promptpreprocess.Observation) error {
	if observer.Logger == nil {
		return errors.New("safe logger is required")
	}
	eventType, result, category := "prompt.preprocessing_failed", "fallback_original", observation.Category
	if observation.Stage == promptpreprocess.StageComplete && observation.Category == promptpreprocess.CategorySuccess {
		eventType, result, category = "prompt.preprocessing_completed", "processed", ""
	} else if observation.Stage == promptpreprocess.StageCache {
		eventType, result, category = "prompt.preprocessing_cached", observation.Category, ""
	}
	fields := map[string]string{
		"provider": string(observation.Provider), "model": observation.Model, "stage": observation.Stage,
		"attempt": strconv.Itoa(observation.Attempts), "model_evidence": observation.ModelEvidence,
	}
	addInputRef(fields, observer.Inputs, observation.MessageID)
	return observer.Logger.Write(safelog.Event{Class: safelog.Service, Type: eventType, Result: result,
		ErrorCategory: category, Error: observation.Error, Fields: fields})
}

func addInputRef(fields map[string]string, inputs observability.InputReferencer, operation string) {
	if inputs != nil {
		if ref := inputs.InputRef(operation); ref != "" {
			fields["input_ref"] = ref
		}
	}
}
