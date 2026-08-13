package runtimehost

import (
	"context"
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
	return CommandResult{Stdout: append([]byte(nil), r.stdout...), ExitCode: r.exitCode}, nil
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

func (r *recordingInputRunner) RunInput(
	_ context.Context,
	input []byte,
	name string,
	args ...string,
) (CommandResult, error) {
	r.calls = append(r.calls, runnerInvocation{
		input: append([]byte(nil), input...), name: name, args: append([]string(nil), args...),
	})
	return CommandResult{}, nil
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
