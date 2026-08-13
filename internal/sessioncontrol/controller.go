// Package sessioncontrol coordinates replicated session intent with execution
// on the node that owns the tmux runtime.
package sessioncontrol

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/Time4Mind/bria/internal/application"
	"github.com/Time4Mind/bria/internal/domain"
	"github.com/Time4Mind/bria/internal/nodecontrol"
	"github.com/Time4Mind/bria/internal/runtimehost"
)

// ErrRuntimeUnavailable is reserved for failures that cannot be retried by
// the controller. Ordinary node timeouts are accepted into its bounded retry
// path and do not block Telegram update processing.
var ErrRuntimeUnavailable = errors.New("session runtime is unavailable")

type Runtime interface {
	Submit(context.Context, runtimehost.Request) (runtimehost.Receipt, error)
	LookupResult(context.Context, runtimehost.Request) (runtimehost.Result, bool, error)
}

type Controller struct {
	service     *application.Service
	runtime     Runtime
	transcripts nodecontrol.TranscriptReader
	files       nodecontrol.SessionFileReader
	ctx         context.Context
	cancel      context.CancelFunc

	pollInterval  time.Duration
	retryInterval time.Duration
	resultWait    time.Duration
	namingWait    time.Duration
	workers       sync.WaitGroup
	namingMu      sync.Mutex
	naming        map[string]bool
}

func NewWithTranscripts(
	service *application.Service,
	runtime Runtime,
	transcripts nodecontrol.TranscriptReader,
) (*Controller, error) {
	if transcripts == nil {
		return nil, errors.New("transcript reader is required")
	}
	controller, err := New(service, runtime)
	if err != nil {
		return nil, err
	}
	controller.transcripts = transcripts
	return controller, nil
}

func (c *Controller) SetSessionFiles(files nodecontrol.SessionFileReader) error {
	if files == nil {
		return errors.New("session file reader is required")
	}
	c.files = files
	return nil
}

func New(service *application.Service, runtime Runtime) (*Controller, error) {
	if service == nil || runtime == nil {
		return nil, errors.New("application service and runtime router are required")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Controller{
		service: service, runtime: runtime, ctx: ctx, cancel: cancel,
		pollInterval: 50 * time.Millisecond, retryInterval: 200 * time.Millisecond,
		resultWait: 15 * time.Second, namingWait: 35 * time.Second,
		naming: make(map[string]bool),
	}, nil
}

func (c *Controller) Shutdown(ctx context.Context) error {
	c.cancel()
	done := make(chan struct{})
	go func() {
		c.workers.Wait()
		close(done)
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-done:
		return nil
	}
}

type Accepted struct {
	Session      domain.SessionRef
	Receipt      runtimehost.Receipt
	NamingQueued bool
	Deferred     bool
}
