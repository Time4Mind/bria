package recoveryruntime_test

import (
	"bufio"
	"context"
	"os"
	"reflect"
	"testing"

	"bria/internal/domain"
	"bria/internal/recoveryruntime"
	"bria/internal/runtimeprotocol"
	"bria/internal/sessionruntime"
)

func TestReaderReconcilesAcceptedTurnsThroughFreshOneShotProcess(t *testing.T) {
	reader, err := recoveryruntime.New(sessionruntime.CommandSpec{
		Path: os.Args[0],
		Args: []string{"-test.run=^TestRecoveryReaderHelperProcess$"},
		Env:  []string{"BRIA_RECOVERY_TEST_HELPER=1"},
	}, recoveryruntime.Options{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	got, err := reader.ReadAcceptedTurns(context.Background(), sessionruntime.AcceptedTurnReadRequest{
		SessionID: "logical-recovery", Provider: domain.ProviderCodex, Workdir: t.TempDir(),
		Binding: domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "provider-recovery", Generation: 3},
	})
	if err != nil {
		t.Fatalf("ReadAcceptedTurns() error = %v", err)
	}
	want := sessionruntime.AcceptedTurnReconciliation{Turns: []sessionruntime.ReconciledAcceptedTurn{
		{MessageID: "m-complete", Outcome: sessionruntime.AcceptedTurnCompleted},
		{MessageID: "m-live", Outcome: sessionruntime.AcceptedTurnUnknown},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ReadAcceptedTurns() = %#v, want %#v", got, want)
	}
}

func TestRecoveryReaderHelperProcess(t *testing.T) {
	if os.Getenv("BRIA_RECOVERY_TEST_HELPER") != "1" {
		return
	}
	if os.Getenv(sessionruntime.EnvironmentStartMode) != "resume" ||
		os.Getenv(sessionruntime.EnvironmentProviderSession) != "provider-recovery" ||
		os.Getenv(sessionruntime.EnvironmentGeneration) != "3" {
		os.Exit(41)
	}
	emitRecoveryFrame(runtimeprotocol.AdapterMessage{
		Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeReady,
		ProviderSessionID: "provider-recovery", Readiness: "protocol", Authentication: "unknown",
	})
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		message, err := runtimeprotocol.DecodeParentLine(scanner.Bytes(), runtimeprotocol.Limits{})
		if err != nil {
			os.Exit(42)
		}
		switch message.Type {
		case runtimeprotocol.TypeReconcileAcceptedTurns:
			emitRecoveryFrame(runtimeprotocol.AdapterMessage{
				Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeAcceptedTurn,
				RequestID: message.RequestID, MessageID: "m-complete", Status: "completed",
			})
			emitRecoveryFrame(runtimeprotocol.AdapterMessage{
				Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeAcceptedTurn,
				RequestID: message.RequestID, MessageID: "m-live", Status: "unknown",
			})
			emitRecoveryFrame(runtimeprotocol.AdapterMessage{
				Protocol: runtimeprotocol.Version, Type: runtimeprotocol.TypeReconciliationCompleted,
				RequestID: message.RequestID,
			})
		case runtimeprotocol.TypeClose:
			return
		default:
			os.Exit(43)
		}
	}
	os.Exit(44)
}

func emitRecoveryFrame(message runtimeprotocol.AdapterMessage) {
	line, err := runtimeprotocol.EncodeAdapterLine(message, runtimeprotocol.Limits{})
	if err != nil {
		os.Exit(45)
	}
	if _, err := os.Stdout.Write(line); err != nil {
		os.Exit(46)
	}
}
