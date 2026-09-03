package codex

import (
	"context"
	"errors"
	"testing"
	"time"

	"bria/internal/authflow"
)

func TestCLIAuthenticatorUsesStdinSecretAndOfficialStatusAsReplayFence(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	runner := &fakeAuthRunner{status: []error{errors.New("not logged in"), nil}}
	auth, err := newCLIAuthenticator(runner, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	begin, err := auth.Begin(context.Background(), authflow.BeginRequest{OperationID: "operation-1", ComputerID: "local", Provider: authflow.ProviderCodex})
	if err != nil || begin.ChallengeReference == "" || !begin.ExpiresAt.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("Begin() = %#v, %v", begin, err)
	}
	secret := authflow.NewSecretPayload([]byte("sk-temporary"))
	err = auth.Complete(context.Background(), authflow.CompleteRequest{OperationID: "operation-1", ComputerID: "local", Provider: authflow.ProviderCodex, ChallengeReference: begin.ChallengeReference, Secret: secret})
	if err != nil {
		t.Fatalf("Complete() error = %v", err)
	}
	if string(runner.secret) != "sk-temporary" {
		t.Fatalf("login stdin = %q", runner.secret)
	}
	runner.status = []error{nil}
	runner.secret = nil
	if err := auth.Complete(context.Background(), authflow.CompleteRequest{OperationID: "operation-1", ComputerID: "local", Provider: authflow.ProviderCodex, ChallengeReference: begin.ChallengeReference, Secret: authflow.NewSecretPayload([]byte("unused"))}); err != nil {
		t.Fatal(err)
	}
	if runner.secret != nil {
		t.Fatal("replay passed a second secret to login")
	}
}

type fakeAuthRunner struct {
	status []error
	secret []byte
}

func (runner *fakeAuthRunner) Status(context.Context) error {
	err := runner.status[0]
	runner.status = runner.status[1:]
	return err
}
func (runner *fakeAuthRunner) Login(_ context.Context, secret []byte) error {
	runner.secret = append([]byte(nil), secret...)
	return nil
}
