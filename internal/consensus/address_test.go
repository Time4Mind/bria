package consensus

import "testing"

func TestAdvertisedAddressValidationDoesNotRequireDNS(t *testing.T) {
	for _, valid := range []string{"node.invalid:7946", "127.0.0.1:1", "[2001:db8::1]:65535"} {
		if err := validateAdvertisedAddress(valid); err != nil {
			t.Fatalf("valid address %q: %v", valid, err)
		}
	}
	for _, invalid := range []string{"", "node.invalid", ":7946", "node.invalid:0", "node.invalid:65536"} {
		if err := validateAdvertisedAddress(invalid); err == nil {
			t.Fatalf("invalid address %q accepted", invalid)
		}
	}
}
