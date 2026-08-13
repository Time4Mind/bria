package telegramui

import "testing"

func FuzzCallbackDecodeNeverAcceptsNonCanonicalOrOversizedData(f *testing.F) {
	for _, seed := range []string{
		"menu", "session:opaque", "unknown:value", "menu:../../node", string(make([]byte, 65)),
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		callback, err := DecodeCallback(value)
		if err != nil {
			return
		}
		encoded, err := callback.Encode()
		if err != nil || encoded != value || len([]byte(encoded)) > MaxCallbackBytes {
			t.Fatalf("non-canonical callback accepted: %q -> %q (%v)", value, encoded, err)
		}
	})
}
