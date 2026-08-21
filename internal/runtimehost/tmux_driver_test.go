package runtimehost

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type runnerInvocation struct {
	input []byte
	name  string
	args  []string
}

type recordingInputRunner struct {
	calls    []runnerInvocation
	stdout   []byte
	exitCode int
	runError error
	inputErr error
}

func (r *recordingInputRunner) LookPath(name string) (string, error) {
	return "/usr/bin/" + name, nil
}

func (r *recordingInputRunner) Run(
	_ context.Context,
	name string,
	args ...string,
) (CommandResult, error) {
	r.calls = append(r.calls, runnerInvocation{name: name, args: append([]string(nil), args...)})
	return CommandResult{Stdout: append([]byte(nil), r.stdout...), ExitCode: r.exitCode}, r.runError
}

func TestTmuxDriverChecksExactRuntimeTarget(t *testing.T) {
	runner := &recordingInputRunner{}
	driver, err := NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	exists, err := driver.TargetExists(context.Background(), "bria:window")
	if err != nil || !exists {
		t.Fatalf("existing target=%v err=%v", exists, err)
	}
	runner.exitCode = 1
	exists, err = driver.TargetExists(context.Background(), "bria:missing")
	if err != nil || exists {
		t.Fatalf("missing target=%v err=%v", exists, err)
	}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, []string{"has-session", "-t", "bria:missing"}) {
		t.Fatalf("target probe args=%v", got)
	}
}

func TestTmuxDriverResizesExistingRuntimeViewport(t *testing.T) {
	runner := &recordingInputRunner{}
	driver, err := NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.ResizeViewport(context.Background(), "bria:window"); err != nil {
		t.Fatal(err)
	}
	want := []string{"resize-window", "-t", "bria:window", "-x", "80", "-y", "40"}
	if got := runner.calls[0].args; !reflect.DeepEqual(got, want) {
		t.Fatalf("resize args=%v, want %v", got, want)
	}
}

func (r *recordingInputRunner) RunInput(
	_ context.Context,
	input []byte,
	name string,
	args ...string,
) (CommandResult, error) {
	r.calls = append(r.calls, runnerInvocation{
		input: append([]byte(nil), input...), name: name, args: append([]string(nil), args...),
	})
	return CommandResult{}, r.inputErr
}

func TestTmuxDriverSendsLiteralInputThroughStdin(t *testing.T) {
	runner := &recordingInputRunner{}
	driver, err := NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	input := "$(touch /tmp/must-not-exist); `id`; line 2"
	if err := driver.SendLiteral(context.Background(), "@9", "operation-1", input); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d, want 3", len(runner.calls))
	}
	if got := string(runner.calls[0].input); got != input {
		t.Fatalf("stdin = %q, want literal %q", got, input)
	}
	wantLoad := []string{"load-buffer", "-b", tmuxBufferName("operation-1"), "-"}
	if !reflect.DeepEqual(runner.calls[0].args, wantLoad) {
		t.Fatalf("load args = %v", runner.calls[0].args)
	}
	wantPaste := []string{
		"paste-buffer", "-d", "-b", tmuxBufferName("operation-1"), "-t", "@9",
	}
	if !reflect.DeepEqual(runner.calls[1].args, wantPaste) {
		t.Fatalf("paste args = %v", runner.calls[1].args)
	}
	if got := runner.calls[2].args; !reflect.DeepEqual(got, []string{"send-keys", "-t", "@9", "Enter"}) {
		t.Fatalf("enter args = %v", got)
	}
}

func TestTmuxDriverDeletesLoadedBufferWhenPasteFails(t *testing.T) {
	runner := &recordingInputRunner{runError: errors.New("tmux unavailable")}
	driver, err := NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.SendLiteral(context.Background(), "@9", "operation-1", "input"); err == nil {
		t.Fatal("expected paste error")
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls = %d, want load, paste, cleanup", len(runner.calls))
	}
	wantDelete := []string{"delete-buffer", "-b", tmuxBufferName("operation-1")}
	if got := runner.calls[2].args; !reflect.DeepEqual(got, wantDelete) {
		t.Fatalf("cleanup args = %v, want %v", got, wantDelete)
	}
}

func TestTmuxDriverDeletesPossibleBufferWhenLoadTransportFails(t *testing.T) {
	runner := &recordingInputRunner{inputErr: errors.New("transport interrupted")}
	driver, err := NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := driver.SendLiteral(context.Background(), "@9", "operation-1", "input"); err == nil {
		t.Fatal("expected load transport error")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls = %d, want load and best-effort cleanup", len(runner.calls))
	}
	wantDelete := []string{"delete-buffer", "-b", tmuxBufferName("operation-1")}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, wantDelete) {
		t.Fatalf("cleanup args = %v, want %v", got, wantDelete)
	}
}

func TestTmuxDriverCapturesANSIAndClosesByExactTarget(t *testing.T) {
	runner := &recordingInputRunner{stdout: []byte("pane")}
	driver, err := NewTmuxDriver(runner, time.Second, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	pane, err := driver.CapturePane(context.Background(), "@12")
	if err != nil || string(pane) != "pane" {
		t.Fatalf("capture: pane=%q err=%v", pane, err)
	}
	if err := driver.Close(context.Background(), "@12"); err != nil {
		t.Fatal(err)
	}
	wantCapture := []string{"capture-pane", "-e", "-p", "-t", "@12"}
	if !reflect.DeepEqual(runner.calls[0].args, wantCapture) {
		t.Fatalf("capture args = %v", runner.calls[0].args)
	}
	if got := runner.calls[1].args; !reflect.DeepEqual(got, []string{"kill-window", "-t", "@12"}) {
		t.Fatalf("close args = %v", got)
	}
}
