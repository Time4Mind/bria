package runtimeprotocol

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestExistingParentMessagesRemainWireCompatible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want ParentMessage
	}{
		{`{"protocol":1,"type":"submit","request_id":"request-1","text":"hello"}`, ParentMessage{Protocol: 1, Type: TypeSubmit, RequestID: "request-1", Text: "hello"}},
		{`{"protocol":1,"type":"submit","request_id":"request-2","text":""}`, ParentMessage{Protocol: 1, Type: TypeSubmit, RequestID: "request-2", Text: ""}},
		{`{"protocol":1,"type":"interrupt","request_id":"request-1"}`, ParentMessage{Protocol: 1, Type: TypeInterrupt, RequestID: "request-1"}},
		{`{"protocol":1,"type":"close"}`, ParentMessage{Protocol: 1, Type: TypeClose}},
	}
	for _, test := range tests {
		got, err := DecodeParentLine([]byte(test.line+"\n"), Limits{})
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("DecodeParentLine(%s) = %#v, %v; want %#v", test.line, got, err, test.want)
		}
		encoded, err := EncodeParentLine(got, Limits{})
		if err != nil || string(encoded) != test.line+"\n" {
			t.Fatalf("EncodeParentLine(%#v) = %q, %v; want %q", got, encoded, err, test.line+"\n")
		}
	}
}

func TestExistingAdapterMessagesRemainWireCompatible(t *testing.T) {
	t.Parallel()
	tests := []struct {
		line string
		want AdapterMessage
	}{
		{`{"protocol":1,"type":"ready","provider_session_id":"provider-1","readiness":"protocol","authentication":"unknown"}`, AdapterMessage{Protocol: 1, Type: TypeReady, ProviderSessionID: "provider-1", Readiness: "protocol", Authentication: "unknown"}},
		{`{"protocol":1,"type":"accepted","request_id":"request-1"}`, AdapterMessage{Protocol: 1, Type: TypeAccepted, RequestID: "request-1"}},
		{`{"protocol":1,"type":"event","request_id":"request-1","kind":"commentary","text":"working"}`, AdapterMessage{Protocol: 1, Type: TypeEvent, RequestID: "request-1", Kind: "commentary", Text: "working"}},
		{`{"protocol":1,"type":"final","request_id":"request-1","text":"done"}`, AdapterMessage{Protocol: 1, Type: TypeFinal, RequestID: "request-1", Text: "done"}},
		{`{"protocol":1,"type":"completed","request_id":"request-1","status":"failed","error_code":"provider_error"}`, AdapterMessage{Protocol: 1, Type: TypeCompleted, RequestID: "request-1", Status: "failed", ErrorCode: "provider_error"}},
	}
	for _, test := range tests {
		got, err := DecodeAdapterLine([]byte(test.line), Limits{})
		if err != nil || !reflect.DeepEqual(got, test.want) {
			t.Fatalf("DecodeAdapterLine(%s) = %#v, %v; want %#v", test.line, got, err, test.want)
		}
		encoded, err := EncodeAdapterLine(got, Limits{})
		if err != nil || string(encoded) != test.line+"\n" {
			t.Fatalf("EncodeAdapterLine(%#v) = %q, %v; want %q", got, encoded, err, test.line+"\n")
		}
	}
}

func TestDurableMessageIdentityRoundTripsOnlyOnSubmitAcceptance(t *testing.T) {
	parent := ParentMessage{Protocol: 1, Type: TypeSubmit, RequestID: "turn-1", MessageID: "telegram:chat:42", Text: "hello"}
	line, err := EncodeParentLine(parent, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decodedParent, err := DecodeParentLine(line, Limits{})
	if err != nil || decodedParent.MessageID != parent.MessageID {
		t.Fatalf("parent = %#v, %v", decodedParent, err)
	}
	accepted := AdapterMessage{Protocol: 1, Type: TypeAccepted, RequestID: "turn-1", MessageID: parent.MessageID}
	line, err = EncodeAdapterLine(accepted, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	decodedAccepted, err := DecodeAdapterLine(line, Limits{})
	if err != nil || decodedAccepted.MessageID != parent.MessageID {
		t.Fatalf("accepted = %#v, %v", decodedAccepted, err)
	}
}

func TestStructuredLocalAttachmentsRoundTripOutsidePromptText(t *testing.T) {
	path := filepath.Join(t.TempDir(), "photo.png")
	want := ParentMessage{
		Protocol: Version, Type: TypeSubmit, RequestID: "turn-photo", MessageID: "telegram:42:10", Text: "inspect",
		Attachments: []LocalAttachment{{
			Path: path, Size: 3,
			SHA256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		}},
	}
	line, err := EncodeParentLine(want, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := DecodeParentLine(line, Limits{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("structured submit = %#v, %v; want %#v", got, err, want)
	}
	if got.Text != "inspect" || strings.Contains(got.Text, path) {
		t.Fatalf("attachment path entered prompt text: %q", got.Text)
	}
	for _, invalid := range []LocalAttachment{
		{Path: "relative/photo.png", Size: 3, SHA256: want.Attachments[0].SHA256},
		{Path: path, Size: 0, SHA256: want.Attachments[0].SHA256},
		{Path: path, Size: 3, SHA256: strings.ToUpper(want.Attachments[0].SHA256)},
	} {
		message := want
		message.Attachments = []LocalAttachment{invalid}
		if _, err := EncodeParentLine(message, Limits{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid attachment %#v error = %v, want ErrProtocol", invalid, err)
		}
	}
}

func TestQuestionInteractionRoundTripsWithDoubleCorrelation(t *testing.T) {
	t.Parallel()
	want := AdapterMessage{
		Protocol: 1, Type: TypeInteractionRequest, RequestID: "request-7",
		InteractionRequest: &InteractionRequest{
			ID: "string:ask-1", Kind: InteractionQuestion, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "item-1", Blocking: true,
			Questions: []Question{{ID: "q1", Header: "Choice", Text: "Pick one", Options: []Option{{Label: "First", Description: "Use first"}}, IsOther: true}},
		},
	}
	line, err := EncodeAdapterLine(want, Limits{})
	if err != nil {
		t.Fatalf("EncodeAdapterLine() error = %v", err)
	}
	got, err := DecodeAdapterLine(line, Limits{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("question round trip = %#v, %v; want %#v", got, err, want)
	}
	if !strings.Contains(string(line), `"request_id":"request-7"`) || !strings.Contains(string(line), `"id":"string:ask-1"`) {
		t.Fatalf("wire lost turn or interaction correlation: %s", line)
	}
}

func TestInteractionDoesNotRequireProviderSpecificIdentifiers(t *testing.T) {
	t.Parallel()
	message := AdapterMessage{
		Protocol: 1, Type: TypeInteractionRequest, RequestID: "request-8",
		InteractionRequest: &InteractionRequest{
			ID: "interaction-1", Kind: InteractionQuestion, Blocking: true,
			Questions: []Question{{ID: "q1", Header: "Choice", Text: "Pick one", Options: []Option{{Label: "First"}}}},
		},
	}
	line, err := EncodeAdapterLine(message, Limits{})
	if err != nil {
		t.Fatalf("provider-neutral interaction error = %v", err)
	}
	got, err := DecodeAdapterLine(line, Limits{})
	if err != nil || !reflect.DeepEqual(got, message) {
		t.Fatalf("provider-neutral interaction round trip = %#v, %v; want %#v", got, err, message)
	}
}

func TestApprovalInteractionsAndResponsesAreTypedAndCorrelated(t *testing.T) {
	t.Parallel()
	requests := []InteractionRequest{
		{ID: "number:901", Kind: InteractionCommandApproval, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "cmd-1", ApprovalID: "approval-1", StartedAtMS: 17, Reason: "needed", Command: "go test ./...", Cwd: "/tmp/work", Decisions: []ApprovalDecision{DecisionAccept, DecisionAcceptForSession, DecisionDecline, DecisionCancel}},
		{ID: "number:902", Kind: InteractionFileApproval, ThreadID: "thread-1", TurnID: "turn-1", ItemID: "file-1", StartedAtMS: 18, Reason: "write", GrantRoot: "/tmp/work", Decisions: []ApprovalDecision{DecisionAccept, DecisionDecline, DecisionCancel}},
	}
	for _, request := range requests {
		message := AdapterMessage{Protocol: 1, Type: TypeInteractionRequest, RequestID: "request-9", InteractionRequest: &request}
		line, err := EncodeAdapterLine(message, Limits{})
		if err != nil {
			t.Fatalf("EncodeAdapterLine(%q) error = %v", request.Kind, err)
		}
		decoded, err := DecodeAdapterLine(line, Limits{})
		if err != nil || !reflect.DeepEqual(decoded, message) {
			t.Fatalf("approval round trip = %#v, %v; want %#v", decoded, err, message)
		}

		response := InteractionResponse{ID: request.ID, Outcome: OutcomeAnswered, Decision: DecisionDecline}
		if err := ValidateResponse(request, response, Limits{}); err != nil {
			t.Fatalf("ValidateResponse(%q) error = %v", request.Kind, err)
		}
		parent := ParentMessage{Protocol: 1, Type: TypeInteractionResponse, RequestID: "request-9", InteractionResponse: &response}
		encoded, err := EncodeParentLine(parent, Limits{})
		if err != nil {
			t.Fatalf("EncodeParentLine(%q) error = %v", request.Kind, err)
		}
		got, err := DecodeParentLine(encoded, Limits{})
		if err != nil || !reflect.DeepEqual(got, parent) {
			t.Fatalf("response round trip = %#v, %v; want %#v", got, err, parent)
		}
	}
}

func TestQuestionAnswerAndCancellationValidation(t *testing.T) {
	t.Parallel()
	request := InteractionRequest{
		ID: "string:ask-1", Kind: InteractionQuestion, ThreadID: "thread", TurnID: "turn", ItemID: "item", Blocking: true,
		Questions: []Question{
			{ID: "q1", Header: "Choice", Text: "Pick", Options: []Option{{Label: "First"}, {Label: "Second"}}},
			{ID: "q2", Header: "Detail", Text: "Explain", IsOther: true},
		},
	}
	answer := InteractionResponse{ID: request.ID, Outcome: OutcomeAnswered, Answers: map[string][]string{"q1": {"First"}, "q2": {"because"}}}
	if err := ValidateResponse(request, answer, Limits{}); err != nil {
		t.Fatalf("valid question answer error = %v", err)
	}
	cancel := InteractionResponse{ID: request.ID, Outcome: OutcomeCancelled}
	if err := ValidateResponse(request, cancel, Limits{}); err != nil {
		t.Fatalf("valid cancellation error = %v", err)
	}

	invalid := []InteractionResponse{
		{ID: "other", Outcome: OutcomeCancelled},
		{ID: request.ID, Outcome: OutcomeCancelled, Answers: map[string][]string{"q1": {"First"}}},
		{ID: request.ID, Outcome: OutcomeAnswered, Answers: map[string][]string{"unknown": {"First"}}},
		{ID: request.ID, Outcome: OutcomeAnswered, Answers: map[string][]string{"q1": {"not-an-option"}, "q2": {"ok"}}},
		{ID: request.ID, Outcome: OutcomeAnswered, Answers: map[string][]string{"q1": {"First"}}},
	}
	for _, response := range invalid {
		if err := ValidateResponse(request, response, Limits{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ValidateResponse(%#v) error = %v, want ErrProtocol", response, err)
		}
	}
}

func TestInteractionExchangeRequiresTurnAndInteractionCorrelation(t *testing.T) {
	t.Parallel()
	interaction := InteractionRequest{
		ID: "string:ask-1", Kind: InteractionQuestion, ThreadID: "thread", TurnID: "turn", ItemID: "item", Blocking: true,
		Questions: []Question{{ID: "q1", Header: "Choice", Text: "Pick", Options: []Option{{Label: "First"}}}},
	}
	request := AdapterMessage{Protocol: 1, Type: TypeInteractionRequest, RequestID: "request-1", InteractionRequest: &interaction}
	responseBody := InteractionResponse{ID: interaction.ID, Outcome: OutcomeAnswered, Answers: map[string][]string{"q1": {"First"}}}
	response := ParentMessage{Protocol: 1, Type: TypeInteractionResponse, RequestID: request.RequestID, InteractionResponse: &responseBody}
	if err := ValidateInteractionExchange(request, response, Limits{}); err != nil {
		t.Fatalf("valid exchange error = %v", err)
	}

	wrongTurn := response
	wrongTurn.RequestID = "request-2"
	if err := ValidateInteractionExchange(request, wrongTurn, Limits{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("wrong-turn exchange error = %v, want ErrProtocol", err)
	}
	wrongInteraction := response
	copyBody := responseBody
	copyBody.ID = "string:ask-2"
	wrongInteraction.InteractionResponse = &copyBody
	if err := ValidateInteractionExchange(request, wrongInteraction, Limits{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("wrong-interaction exchange error = %v, want ErrProtocol", err)
	}
}

func TestInteractionResponseAcceptanceRoundTripsExactProviderCorrelation(t *testing.T) {
	t.Parallel()
	want := AdapterMessage{
		Protocol: Version, Type: TypeInteractionResponseAccepted,
		ProviderSessionID: "provider-thread-1", RequestID: "turn-operation-7",
		MessageID: "telegram:42:9", InteractionID: "provider-question-3",
	}
	line, err := EncodeAdapterLine(want, Limits{})
	if err != nil {
		t.Fatalf("EncodeAdapterLine() error = %v", err)
	}
	got, err := DecodeAdapterLine(line, Limits{})
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("interaction acceptance = %#v, %v; want %#v", got, err, want)
	}
	for _, invalid := range []AdapterMessage{
		{Protocol: Version, Type: TypeInteractionResponseAccepted, ProviderSessionID: "provider-thread-1", RequestID: "turn-operation-7", InteractionID: "provider-question-3"},
		{Protocol: Version, Type: TypeInteractionResponseAccepted, ProviderSessionID: "provider-thread-1", RequestID: "turn-operation-7", MessageID: "telegram:42:9"},
	} {
		if _, err := EncodeAdapterLine(invalid, Limits{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("invalid acceptance %#v error = %v", invalid, err)
		}
	}
}

func TestAcceptedTurnReconciliationFramesRoundTripWithoutContent(t *testing.T) {
	t.Parallel()
	parent := ParentMessage{Protocol: Version, Type: TypeReconcileAcceptedTurns, RequestID: "reconcile-1"}
	line, err := EncodeParentLine(parent, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := DecodeParentLine(line, Limits{}); err != nil || !reflect.DeepEqual(got, parent) {
		t.Fatalf("reconcile request = %#v, %v", got, err)
	}
	frames := []AdapterMessage{
		{Protocol: Version, Type: TypeAcceptedTurn, RequestID: "reconcile-1", MessageID: "telegram:42:1", Status: "completed"},
		{Protocol: Version, Type: TypeAcceptedTurn, RequestID: "reconcile-1", MessageID: "telegram:42:2", Status: "unknown"},
		{Protocol: Version, Type: TypeReconciliationCompleted, RequestID: "reconcile-1"},
	}
	for _, frame := range frames {
		line, err := EncodeAdapterLine(frame, Limits{})
		if err != nil {
			t.Fatalf("encode %#v: %v", frame, err)
		}
		if got, err := DecodeAdapterLine(line, Limits{}); err != nil || !reflect.DeepEqual(got, frame) {
			t.Fatalf("frame round trip = %#v, %v; want %#v", got, err, frame)
		}
	}
}

func TestCodecRejectsUnknownDuplicateMismatchedAndOversizedPayloads(t *testing.T) {
	t.Parallel()
	invalidAdapter := []string{
		`{"protocol":1,"type":"accepted","request_id":"r","extra":true}`,
		`{"protocol":1,"type":"accepted","request_id":"r","status":""}`,
		`{"protocol":1,"type":"event","request_id":"r","kind":"commentary"}`,
		`{"protocol":1,"type":"accepted","request_id":"r","request_id":"other"}`,
		`{"protocol":1,"type":"interaction_request","request_id":"r","interaction_request":{"id":"i","id":"other","kind":"question","thread_id":"t","turn_id":"u","item_id":"x","questions":[]}}`,
		`{"protocol":1,"type":"ready","provider_session_id":"p","readiness":"protocol","authentication":"confirmed"}`,
	}
	if _, err := DecodeParentLine([]byte(`{"protocol":1,"type":"submit","request_id":"r"}`), Limits{}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("submit without required text error = %v, want ErrProtocol", err)
	}
	for _, line := range invalidAdapter {
		if _, err := DecodeAdapterLine([]byte(line), Limits{}); !errors.Is(err, ErrProtocol) {
			t.Fatalf("DecodeAdapterLine(%s) error = %v, want ErrProtocol", line, err)
		}
	}

	oversized := ParentMessage{Protocol: 1, Type: TypeSubmit, RequestID: "r", Text: strings.Repeat("x", 17)}
	if _, err := EncodeParentLine(oversized, Limits{MaxLineBytes: 128, MaxTextBytes: 16, MaxQuestions: 1, MaxOptionsPerQuestion: 1, MaxAnswersPerQuestion: 1}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized text error = %v, want ErrProtocol", err)
	}
	if _, err := DecodeParentLine([]byte(strings.Repeat(" ", 129)), Limits{MaxLineBytes: 128, MaxTextBytes: 16, MaxQuestions: 1, MaxOptionsPerQuestion: 1, MaxAnswersPerQuestion: 1}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("oversized line error = %v, want ErrProtocol", err)
	}
}
