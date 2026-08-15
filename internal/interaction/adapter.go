// Package interaction defines the transport-neutral lifecycle boundary for
// user-facing adapters. Telegram, web, desktop, or another interface owns its
// protocol parsing and rendering outside the Bria domain and consensus core.
package interaction

import "context"

type Adapter interface {
	Name() string
	Run(context.Context) error
}

type Failure struct {
	Adapter string
	Err     error
}

func Start(ctx context.Context, adapters ...Adapter) <-chan Failure {
	if len(adapters) == 0 {
		return nil
	}
	errors := make(chan Failure, len(adapters))
	for _, adapter := range adapters {
		adapter := adapter
		go func() {
			errors <- Failure{Adapter: adapter.Name(), Err: adapter.Run(ctx)}
		}()
	}
	return errors
}

type Func struct {
	AdapterName string
	RunFunc     func(context.Context) error
}

func (a Func) Name() string { return a.AdapterName }

func (a Func) Run(ctx context.Context) error { return a.RunFunc(ctx) }
