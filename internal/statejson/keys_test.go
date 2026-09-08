package statejson_test

import (
	"strings"
	"testing"

	"bria/internal/statejson"
)

// Characterize the unchanged storage key validator after responsibility extraction.
func TestPersistedKeysRejectAmbiguityAtEveryNestingLevel(t *testing.T) {
	for _, tc := range []struct {
		text  string
		valid bool
	}{
		{`{"version":1,"cards":[{"id":"a"},{"id":"b"}]}`, true},
		{`{"a":{"b":1},"b":2}`, true},
		{`{"version":1,"version":2}`, false},
		{`{"a":[{"id":1,"\u0069d":2}]}`, false},
		{`{"Version":1}`, false},
		{`{"a":{"B":1}}`, false},
		{`{"a":[1,2}`, false},
		{`{"a":`, false},
	} {
		if err := statejson.ValidateKeys(strings.NewReader(tc.text)); (err == nil) != tc.valid {
			t.Errorf("valid=%v err=%v", tc.valid, err)
		}
	}
}
