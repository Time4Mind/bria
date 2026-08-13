package main

import (
	"testing"

	"github.com/Time4Mind/bria/internal/config"
)

func TestLocalControlAddressUsesLoopbackForWildcardBind(t *testing.T) {
	for _, test := range []struct {
		bind string
		want string
	}{
		{bind: "0.0.0.0:7947", want: "127.0.0.1:7947"},
		{bind: "[::]:7947", want: "[::1]:7947"},
		{bind: "127.0.0.1:7947", want: "127.0.0.1:7947"},
	} {
		got, err := localControlAddress(config.Config{ControlBind: test.bind})
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("local control address for %q=%q, want %q", test.bind, got, test.want)
		}
	}
}
