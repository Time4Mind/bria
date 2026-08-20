package telegrambot

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var errLeadershipLost = errors.New("leadership lost during Telegram poll")

type PollerConfig struct {
	API                     API
	Leadership              Leadership
	Cursor                  Cursor
	Handler                 UpdateHandler
	LongPollTimeout         time.Duration
	PollRequestTimeout      time.Duration
	LeadershipCheckInterval time.Duration
	RetryDelay              time.Duration
	MaxCallbackAttempts     int
	OnLeaderActivated       func(context.Context) error
	OnCallbackDropped       func(IncomingUpdate, error, int)
	OnError                 func(string, error)
}

type Poller struct {
	api                 API
	leadership          Leadership
	cursor              Cursor
	handler             UpdateHandler
	longPollSecs        int
	pollRequestTimeout  time.Duration
	leadershipTick      time.Duration
	retryDelay          time.Duration
	offset              int64
	maxCallbackAttempts int
	onLeaderActivated   func(context.Context) error
	onCallbackDropped   func(IncomingUpdate, error, int)
	onError             func(string, error)
	callbackAttempts    map[int64]int
	lastFailure         string
}

func NewPoller(config PollerConfig) (*Poller, error) {
	if config.API == nil || config.Leadership == nil || config.Cursor == nil ||
		config.Handler == nil {
		return nil, errors.New("Telegram poller dependencies are required")
	}
	longPoll := config.LongPollTimeout
	if longPoll <= 0 {
		longPoll = 30 * time.Second
	}
	if longPoll < time.Second || longPoll > 50*time.Second {
		return nil, errors.New("Telegram long-poll timeout must be between 1s and 50s")
	}
	pollRequestTimeout := config.PollRequestTimeout
	if pollRequestTimeout <= 0 {
		pollRequestTimeout = longPoll + 10*time.Second
	}
	leadershipTick := config.LeadershipCheckInterval
	if leadershipTick <= 0 {
		leadershipTick = 250 * time.Millisecond
	}
	retryDelay := config.RetryDelay
	if retryDelay <= 0 {
		retryDelay = time.Second
	}
	maxCallbackAttempts := config.MaxCallbackAttempts
	if maxCallbackAttempts == 0 {
		maxCallbackAttempts = 5
	}
	if maxCallbackAttempts < 1 || maxCallbackAttempts > 100 {
		return nil, errors.New("Telegram callback attempts must be between 1 and 100")
	}
	return &Poller{
		api: config.API, leadership: config.Leadership, cursor: config.Cursor,
		handler:      config.Handler,
		longPollSecs: int(longPoll / time.Second), pollRequestTimeout: pollRequestTimeout,
		leadershipTick: leadershipTick,
		retryDelay:     retryDelay, maxCallbackAttempts: maxCallbackAttempts,
		onLeaderActivated: config.OnLeaderActivated,
		onCallbackDropped: config.OnCallbackDropped,
		onError:           config.OnError,
		callbackAttempts:  make(map[int64]int),
	}, nil
}

// Run polls only while this process is leader. A leadership loss cancels the
// in-flight HTTP request and drops any response racing with that loss.
func (p *Poller) Run(ctx context.Context) error {
	leaderActive := false
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !p.leadership.IsLeader() {
			leaderActive = false
			if err := waitOrDone(ctx, p.leadershipTick); err != nil {
				return err
			}
			continue
		}
		if !leaderActive {
			if p.onLeaderActivated != nil {
				if err := p.onLeaderActivated(ctx); err != nil {
					p.reportFailure("activation", err)
					if err := waitOrDone(ctx, p.retryDelay); err != nil {
						return err
					}
					continue
				}
			}
			offset, err := p.cursor.Load(ctx)
			if err != nil {
				return fmt.Errorf("load Telegram update cursor: %w", err)
			}
			if offset < 0 {
				return errors.New("Telegram update cursor cannot be negative")
			}
			p.offset = offset
			leaderActive = true
		}
		updates, err := p.getUpdatesWhileLeader(ctx)
		if errors.Is(err, errLeadershipLost) {
			leaderActive = false
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			p.reportFailure("get_updates", err)
			if err := waitOrDone(ctx, p.retryDelay); err != nil {
				return err
			}
			continue
		}
		p.clearFailure()
		if !p.leadership.IsLeader() {
			continue
		}
		for _, update := range updates {
			if !p.leadership.IsLeader() {
				leaderActive = false
				break
			}
			if update.UpdateID < p.offset {
				continue
			}
			incoming, parseErr := ParsePrivateDM(update)
			if parseErr == nil {
				handleErr := p.handler.HandleTelegramUpdate(ctx, incoming)
				if handleErr != nil && !p.dropFailedCallback(incoming, handleErr) {
					leaderActive = false
					if err := waitOrDone(ctx, p.retryDelay); err != nil {
						return err
					}
					break
				}
			}
			nextOffset := update.UpdateID + 1
			if nextOffset < 0 {
				return errors.New("Telegram update cursor overflow")
			}
			if err := p.cursor.Commit(ctx, nextOffset); err != nil {
				leaderActive = false
				if err := waitOrDone(ctx, p.retryDelay); err != nil {
					return err
				}
				break
			}
			delete(p.callbackAttempts, update.UpdateID)
			p.offset = nextOffset
		}
	}
}

func (p *Poller) dropFailedCallback(update IncomingUpdate, err error) bool {
	if update.Kind != IncomingCallback {
		return false
	}
	// Replaying a callback immediately cannot clear Telegram flood control and
	// used to turn one rejected edit into five more requests. The handler has
	// already attempted the send-based navigation fallback; consume the update
	// now so newer user actions are not held behind it.
	if _, limited := FloodWait(err); limited {
		if p.onCallbackDropped != nil {
			p.onCallbackDropped(update, err, 1)
		}
		return true
	}
	attempts := p.callbackAttempts[update.UpdateID] + 1
	p.callbackAttempts[update.UpdateID] = attempts
	if attempts < p.maxCallbackAttempts {
		return false
	}
	if attempts == p.maxCallbackAttempts && p.onCallbackDropped != nil {
		p.onCallbackDropped(update, err, attempts)
	}
	return true
}

func (p *Poller) getUpdatesWhileLeader(ctx context.Context) ([]Update, error) {
	pollCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		updates []Update
		err     error
	}
	completed := make(chan result, 1)
	go func() {
		updates, err := p.api.GetUpdates(pollCtx, GetUpdatesRequest{
			Offset: p.offset, Limit: 100, Timeout: p.longPollSecs,
		})
		completed <- result{updates: updates, err: err}
	}()
	leadershipTicker := time.NewTicker(p.leadershipTick)
	defer leadershipTicker.Stop()
	deadline := time.NewTimer(p.pollRequestTimeout)
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-leadershipTicker.C:
			if !p.leadership.IsLeader() {
				return nil, errLeadershipLost
			}
		case <-deadline.C:
			if !p.leadership.IsLeader() {
				return nil, errLeadershipLost
			}
			return nil, context.DeadlineExceeded
		case result := <-completed:
			if !p.leadership.IsLeader() {
				return nil, errLeadershipLost
			}
			return result.updates, result.err
		}
	}
}

func (p *Poller) reportFailure(stage string, err error) {
	failure := stage + ": " + err.Error()
	if failure == p.lastFailure {
		return
	}
	p.lastFailure = failure
	if p.onError != nil {
		p.onError(stage, err)
	}
}

func (p *Poller) clearFailure() { p.lastFailure = "" }

func waitOrDone(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
