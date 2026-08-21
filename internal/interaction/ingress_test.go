package interaction

import (
	"context"
	"strings"
	"testing"
)

func TestIngressIsDeterministicOpaqueAndContextScoped(t *testing.T) {
	first, err := NewIngress("test-ui", "provider-event-42", "message")
	if err != nil {
		t.Fatal(err)
	}
	again, err := NewIngress("test-ui", "provider-event-42", "message")
	if err != nil {
		t.Fatal(err)
	}
	other, err := NewIngress("other-ui", "provider-event-42", "message")
	if err != nil {
		t.Fatal(err)
	}
	if first.ID() != again.ID() || first.ID() == other.ID() ||
		strings.Contains(first.ID(), "provider-event") {
		t.Fatalf("first=%+v again=%+v other=%+v", first, again, other)
	}
	ctx := WithIngress(context.Background(), first)
	got, ok := IngressFromContext(ctx)
	if !ok || got != first {
		t.Fatalf("ingress=%+v ok=%t", got, ok)
	}
	nested := WithIngress(ctx, other)
	if got, ok := IngressFromContext(nested); !ok || got != other {
		t.Fatalf("nested ingress=%+v ok=%t", got, ok)
	}
	if got, ok := IngressFromContext(ctx); !ok || got != first {
		t.Fatalf("parent ingress changed=%+v ok=%t", got, ok)
	}
}

func TestIngressRejectsUnsafeOrUnboundedIdentity(t *testing.T) {
	for _, test := range []struct{ adapter, id, kind string }{
		{"", "42", "message"},
		{"bad adapter", "42", "message"},
		{"test-ui", "", "message"},
		{"test-ui", strings.Repeat("x", 257), "message"},
		{"test-ui", "42", "bad kind"},
	} {
		if _, err := NewIngress(test.adapter, test.id, test.kind); err == nil {
			t.Fatalf("accepted adapter=%q id_len=%d kind=%q", test.adapter, len(test.id), test.kind)
		}
	}
}
