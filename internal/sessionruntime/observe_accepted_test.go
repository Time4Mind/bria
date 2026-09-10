package sessionruntime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"bria/internal/app"
	"bria/internal/domain"
	"bria/internal/sessionruntime"
)

func TestAttachClassifiesOnlyProvenTerminalUnavailability(t *testing.T) {
	spec := sessionruntime.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestObserveAdapterHelper"}, Env: []string{"BRIA_OBSERVE_HELPER=1", "BRIA_OBSERVE_UNAVAILABLE=1"}, PersistentTerminal: true}
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "persisted", Generation: 7}
	_, err = starter.Attach(context.Background(), resumeRequest(testRequest(t.TempDir(), "unavailable"), prior))
	if !errors.Is(err, app.ErrTerminalUnavailable) || sessionruntime.StartupFailureClass(err) != "terminal_unavailable" {
		t.Fatalf("unavailable attach classification = %v class=%q", err, sessionruntime.StartupFailureClass(err))
	}
}

func TestAttachObservesAcceptedWithoutSubmittingAndShutdownDetaches(t *testing.T) {
	spec := sessionruntime.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestObserveAdapterHelper"}, Env: []string{"BRIA_OBSERVE_HELPER=1"}, PersistentTerminal: true}
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "persisted", Generation: 7}
	request := resumeRequest(testRequest(t.TempDir(), "observe"), prior)
	binding, err := starter.Attach(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if binding.SessionID != prior.SessionID || binding.Generation != 8 {
		t.Fatal(binding)
	}
	var eventID string
	result, err := starter.ObserveAcceptedWithCallbacks(ctx, request.SessionID, binding, sessionruntime.TurnCallbacks{MessageID: "accepted-42", OnEvent: func(event sessionruntime.TurnEvent) error { eventID = event.ID; return nil }})
	if err != nil || result.Final != "finished" || eventID != "42:0" {
		t.Fatalf("%+v %s %v", result, eventID, err)
	}
	if err := starter.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(request.Workdir + "/termination")
	if err != nil || string(got) != "detach" {
		t.Fatalf("termination %s %v", got, err)
	}
	if err := starter.Abort(context.Background(), request, binding); err == nil {
		t.Fatal("detached observer tombstone falsely confirmed terminal close")
	}
}

func TestObserveAdapterHelper(t *testing.T) {
	if os.Getenv("BRIA_OBSERVE_HELPER") != "1" {
		return
	}
	if os.Getenv("BRIA_ATTACH_ONLY") != "1" || os.Getenv("BRIA_PERSISTENT_TERMINAL") != "1" {
		os.Exit(42)
	}
	if os.Getenv("BRIA_OBSERVE_UNAVAILABLE") == "1" {
		fmt.Fprintln(os.Stderr, "bria-native-startup:open_terminal:terminal_unavailable")
		os.Exit(1)
	}
	fmt.Println(`{"protocol":1,"type":"ready","provider_session_id":"persisted","readiness":"protocol","authentication":"unknown"}`)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var r map[string]any
		if json.Unmarshal(scanner.Bytes(), &r) != nil {
			os.Exit(43)
		}
		switch r["type"] {
		case "observe_accepted":
			if len(r) != 4 || r["message_id"] != "accepted-42" {
				os.Exit(44)
			}
			id := r["request_id"]
			if os.WriteFile("wire-command", []byte("observe_accepted"), 0600) != nil {
				os.Exit(48)
			}
			fmt.Printf("{\"protocol\":1,\"type\":\"accepted\",\"request_id\":%q,\"message_id\":\"accepted-42\"}\n", id)
			if os.Getenv("BRIA_OBSERVE_HOLD") == "1" {
				continue
			}
			fmt.Printf("{\"protocol\":1,\"type\":\"event\",\"request_id\":%q,\"kind\":\"commentary\",\"text\":\"progress\",\"event_id\":\"42:0\"}\n", id)
			if os.Getenv("BRIA_OBSERVE_LONG") == "1" {
				for i := 1; i < 700; i++ {
					fmt.Printf("{\"protocol\":1,\"type\":\"event\",\"request_id\":%q,\"kind\":\"commentary\",\"text\":\"progress\",\"event_id\":\"42:%d\"}\n", id, i)
				}
			}
			fmt.Printf("{\"protocol\":1,\"type\":\"final\",\"request_id\":%q,\"text\":\"finished\"}\n", id)
			fmt.Printf("{\"protocol\":1,\"type\":\"completed\",\"request_id\":%q,\"status\":\"completed\"}\n", id)
		case "detach", "close":
			if r["type"] == "close" && os.Getenv("BRIA_CLOSE_ACK") == "1" {
				fmt.Println(`{"protocol":1,"type":"closed","provider_session_id":"persisted"}`)
			}
			if os.WriteFile("termination", []byte(r["type"].(string)), 0600) != nil {
				os.Exit(45)
			}
			os.Exit(0)
		default:
			_ = os.WriteFile("wire-command", []byte(fmt.Sprint(r["type"])), 0600)
			os.Exit(46)
		}
	}
	os.Exit(47)
}

func TestAttachRetriesRolledBackObserverWithFreshGeneration(t *testing.T) {
	spec := sessionruntime.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestObserveAdapterHelper"}, Env: []string{"BRIA_OBSERVE_HELPER=1"}, PersistentTerminal: true}
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	defer starter.Shutdown(context.Background())
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "persisted", Generation: 7}
	request := resumeRequest(testRequest(t.TempDir(), "rollback"), prior)
	for _, generation := range []uint64{8, 9, 10} {
		binding, err := starter.Attach(ctx, request)
		if err != nil || binding.Generation != generation {
			t.Fatalf("generation %d: binding=%+v err=%v", generation, binding, err)
		}
		if err := starter.Detach(ctx, request, binding); err != nil {
			t.Fatal(err)
		}
	}
	wrong := prior
	wrong.Generation = 6
	if _, err := starter.Attach(ctx, resumeRequest(request, wrong)); err == nil {
		t.Fatal("unrelated old generation accepted")
	}
}

func TestPersistentCloseRequiresExactPhysicalTerminalAcknowledgement(t *testing.T) {
	for _, ack := range []bool{false, true} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			spec := sessionruntime.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestObserveAdapterHelper"}, Env: []string{"BRIA_OBSERVE_HELPER=1"}, PersistentTerminal: true}
			if ack {
				spec.Env = append(spec.Env, "BRIA_CLOSE_ACK=1")
			}
			starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "persisted", Generation: 7}
			request := resumeRequest(testRequest(t.TempDir(), "close"), prior)
			binding, err := starter.Attach(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			err = starter.Abort(ctx, request, binding)
			if (err == nil) != ack {
				t.Fatalf("ack=%t close=%v", ack, err)
			}
			if replay := starter.Abort(ctx, request, binding); (replay == nil) != ack {
				t.Fatalf("ack=%t replayclose=%v", ack, replay)
			}
		})
	}
}

func TestObservationCancellationNeverInterruptsAcceptedCLI(t *testing.T) {
	spec := sessionruntime.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestObserveAdapterHelper"}, Env: []string{"BRIA_OBSERVE_HELPER=1", "BRIA_OBSERVE_HOLD=1"}, PersistentTerminal: true}
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "persisted", Generation: 7}
	request := resumeRequest(testRequest(t.TempDir(), "cancel"), prior)
	binding, err := starter.Attach(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	stale := binding
	stale.Generation++
	if _, err := starter.ObserveAcceptedWithCallbacks(ctx, request.SessionID, stale, sessionruntime.TurnCallbacks{MessageID: "accepted-42"}); err == nil {
		t.Fatal("stale binding observed")
	}
	if _, err := os.Stat(request.Workdir + "/wire-command"); !os.IsNotExist(err) {
		t.Fatal("stale binding sent a wire command")
	}
	_, err = starter.ObserveAcceptedWithCallbacks(ctx, request.SessionID, binding, sessionruntime.TurnCallbacks{MessageID: "accepted-42", OnAccepted: func(string) error { cancel(); return nil }})
	if err != context.Canceled {
		t.Fatalf("cancel=%v", err)
	}
	got, err := os.ReadFile(request.Workdir + "/wire-command")
	if err != nil || string(got) != "observe_accepted" {
		t.Fatalf("sent command=%s %v", got, err)
	}
}

func TestObservedLongTurnStreamsBeyondRetainedEventWindow(t *testing.T) {
	spec := sessionruntime.CommandSpec{Path: os.Args[0], Args: []string{"-test.run=TestObserveAdapterHelper"}, Env: []string{"BRIA_OBSERVE_HELPER=1", "BRIA_OBSERVE_LONG=1"}, PersistentTerminal: true}
	starter, err := sessionruntime.NewStarter(map[domain.Provider]sessionruntime.CommandSpec{domain.ProviderCodex: spec}, sessionruntime.Options{MaxTurnEvents: 2})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	prior := domain.ProviderBinding{Provider: domain.ProviderCodex, SessionID: "persisted", Generation: 7}
	request := resumeRequest(testRequest(t.TempDir(), "long"), prior)
	binding, err := starter.Attach(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	defer starter.Shutdown(context.Background())
	count := 0
	result, err := starter.ObserveAcceptedWithCallbacks(ctx, request.SessionID, binding, sessionruntime.TurnCallbacks{MessageID: "accepted-42", OnEvent: func(sessionruntime.TurnEvent) error { count++; return nil }})
	if err != nil || count != 700 || result.Final != "finished" || len(result.Events) > 2 {
		t.Fatalf("count=%d retained=%d final=%q err=%v", count, len(result.Events), result.Final, err)
	}
}
