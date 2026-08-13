package consensus

import (
	"errors"
	"net"
	"strconv"
	"strings"
)

// validateAdvertisedAddress checks only syntax. Resolving a stable hostname at
// process startup would couple Raft identity configuration to transient DNS.
func validateAdvertisedAddress(address string) error {
	host, rawPort, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errors.New("advertised address must contain a host and numeric port")
	}
	port, err := strconv.Atoi(rawPort)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("advertised address port is invalid")
	}
	return nil
}
