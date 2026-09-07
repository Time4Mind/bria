// Package providermodels reads the local provider's advertised model catalog.
package providermodels

import (
	"context"
	"errors"
	"time"

	"bria/internal/config"
	"bria/internal/domain"
	"bria/internal/processenv"
	"bria/internal/settingsport"
)

var ErrUnsupported = errors.New("model catalog unsupported for this node or CLI")

type Model = settingsport.Model

// Service serializes bounded catalog reads and caches successful results only.
type Service struct {
	nodeID      domain.ComputerID
	executable  string
	arguments   []string
	environment []string
	gate        chan struct{}
	cached      []Model
	expires     time.Time
}

func FromConfig(configuration config.Config, environment []string) (*Service, error) {
	if configuration.Computer == nil || configuration.Computer.ID == "" {
		return nil, errors.New("model catalog requires a local computer ID")
	}
	safe, err := processenv.Build(environment, processenv.Options{TelegramTokenEnv: configuration.TelegramToken.EnvVar})
	if err != nil {
		return nil, err
	}
	s := &Service{nodeID: domain.ComputerID(configuration.Computer.ID), environment: safe, gate: make(chan struct{}, 1)}
	if command, ok := configuration.EnabledCommand(domain.ProviderCodex); ok {
		s.executable = command.Exec
		s.arguments = append([]string(nil), command.Argv...)
	}
	return s, nil
}

func (s *Service) Models(ctx context.Context, nodeID domain.ComputerID, provider domain.Provider) ([]Model, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s == nil || nodeID != s.nodeID || provider != domain.ProviderCodex || s.executable == "" {
		return nil, ErrUnsupported
	}
	// Include waiting for another collection in the total bound.
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case s.gate <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-s.gate }()
	if time.Now().Before(s.expires) {
		return clone(s.cached), nil
	}
	models, err := s.collect(ctx)
	if err != nil {
		return nil, err
	}
	s.cached = clone(models)
	s.expires = time.Now().Add(time.Minute)
	return models, nil
}

func clone(models []Model) []Model {
	result := append([]Model(nil), models...)
	for i := range result {
		result[i].Efforts = append([]string(nil), result[i].Efforts...)
	}
	return result
}
