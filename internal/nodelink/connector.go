package nodelink

import (
	"context"
	"errors"
	"strings"
	"time"
)

type ExecutorConnector struct {
	dialer     RawDialer
	address    string
	identity   TLSIdentity
	maxFrame   uint32
	retryDelay time.Duration
}

func NewExecutorConnector(dialer RawDialer, coordinatorAddress string, identity TLSIdentity, maxFrameBytes uint32, retryDelay time.Duration) (*ExecutorConnector, error) {
	if dialer == nil || strings.TrimSpace(coordinatorAddress) == "" || retryDelay <= 0 || identity.ExecutorComputerID != identity.LocalComputerID {
		return nil, ErrInvalidTLSIdentity
	}
	if _, err := pinnedTLSConfig(identity, false); err != nil {
		return nil, err
	}
	if maxFrameBytes == 0 {
		maxFrameBytes = DefaultMaxFrameBytes
	}
	return &ExecutorConnector{dialer: dialer, address: coordinatorAddress, identity: identity, maxFrame: maxFrameBytes, retryDelay: retryDelay}, nil
}

// Run maintains exactly one executor-initiated channel to the configured,
// pinned coordinator. Connection failures cause a retry to that same endpoint;
// there is deliberately no candidate list, election, or coordinator mutation.
func (connector *ExecutorConnector) Run(ctx context.Context, handle func(context.Context, *SecureChannel) error) error {
	if connector == nil || handle == nil {
		return ErrInvalidTLSIdentity
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		channel, err := DialCoordinator(ctx, connector.dialer, connector.address, connector.identity, connector.maxFrame)
		if err == nil {
			handleErr := handle(ctx, channel)
			_ = channel.Close()
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(handleErr, ErrWrongCertificate) {
				return handleErr
			}
		} else if errors.Is(err, ErrWrongCertificate) || errors.Is(err, ErrInvalidTLSIdentity) {
			return err
		}
		timer := time.NewTimer(connector.retryDelay)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}
