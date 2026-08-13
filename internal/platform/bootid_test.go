package platform

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakePlatformRunner struct {
	result CommandResult
	err    error
	name   string
	args   []string
	wait   bool
}

func (runner *fakePlatformRunner) Run(ctx context.Context, name string, args ...string) (CommandResult, error) {
	runner.name = name
	runner.args = append([]string(nil), args...)
	if runner.wait {
		<-ctx.Done()
		return CommandResult{}, ctx.Err()
	}
	return runner.result, runner.err
}

func TestDarwinBootIDProviderUsesFixedSysctlArgv(t *testing.T) {
	t.Parallel()
	runner := &fakePlatformRunner{result: CommandResult{
		Stdout: []byte("{ sec = 1712345678, usec = 42 } Mon Apr  5 00:00:00 2026\n"),
	}}
	provider := NewDarwinBootIDProvider(runner, time.Second)

	got, err := provider.Current(context.Background())
	if err != nil {
		t.Fatalf("Current(): %v", err)
	}
	if got != "darwin:1712345678:000042" {
		t.Fatalf("Current() = %q", got)
	}
	if runner.name != "sysctl" || !reflect.DeepEqual(runner.args, []string{"-n", "kern.boottime"}) {
		t.Fatalf("command = %q %#v", runner.name, runner.args)
	}
}

func TestDarwinBootIDProviderHonorsTimeout(t *testing.T) {
	t.Parallel()
	provider := NewDarwinBootIDProvider(&fakePlatformRunner{wait: true}, time.Millisecond)

	_, err := provider.Current(context.Background())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Current() error = %v, want deadline exceeded", err)
	}
}

func TestUnsupportedBootIDProvider(t *testing.T) {
	t.Parallel()
	_, err := (UnsupportedBootIDProvider{}).Current(context.Background())
	if !errors.Is(err, ErrBootIDUnsupported) {
		t.Fatalf("Current() error = %v, want unsupported", err)
	}
}
