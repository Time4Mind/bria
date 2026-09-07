package telegramruntimecomposition

import (
	"reflect"
	"testing"

	"bria/internal/domain"
	"bria/internal/telegramcontroller"
)

func TestNativeSurfaceProjectionRetainsExactCarrierSession(t *testing.T) {
	id := domain.SessionID("123e4567-e89b-12d3-a456-426614174000")
	surface, err := projectSemanticSurface(telegramcontroller.SemanticSurface{Text: "Select model", NativeSessionID: id, Rows: [][]telegramcontroller.SemanticButton{{{Label: "↑", Action: telegramcontroller.SemanticNativeKey, SessionID: id, Choice: 1}}}})
	if err != nil {
		t.Fatal(err)
	}
	field := reflect.ValueOf(surface).Elem().FieldByName("NativeSessionID")
	if !field.IsValid() || field.Interface() != id {
		t.Fatal("native surface lost exact session carrier identity")
	}
}
