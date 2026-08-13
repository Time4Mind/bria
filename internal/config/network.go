package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

func (c Config) validateRaftPeers() error {
	ids := make(map[string]struct{}, len(c.RaftPeers))
	addresses := make(map[string]struct{}, len(c.RaftPeers))
	controlAddresses := make(map[string]struct{}, len(c.RaftPeers))
	selfAddress := ""
	for index, peer := range c.RaftPeers {
		prefix := fmt.Sprintf("raft_peers[%d]", index)
		if strings.TrimSpace(peer.NodeID) == "" {
			return fmt.Errorf("%s.node_id is required", prefix)
		}
		if strings.TrimSpace(peer.NodeName) == "" {
			return fmt.Errorf("%s.node_name is required", prefix)
		}
		if _, exists := ids[peer.NodeID]; exists {
			return fmt.Errorf("duplicate raft peer node_id %q", peer.NodeID)
		}
		ids[peer.NodeID] = struct{}{}
		if _, exists := addresses[peer.Address]; exists {
			return fmt.Errorf("duplicate raft peer address %q", peer.Address)
		}
		addresses[peer.Address] = struct{}{}
		if err := validateHostPort(prefix+".address", peer.Address, false); err != nil {
			return err
		}
		controlAddress, err := peer.EffectiveControlAddress()
		if err != nil {
			return fmt.Errorf("%s.control_address: %w", prefix, err)
		}
		if err := validateHostPort(prefix+".control_address", controlAddress, false); err != nil {
			return err
		}
		if _, exists := controlAddresses[controlAddress]; exists {
			return fmt.Errorf("duplicate control address %q", controlAddress)
		}
		controlAddresses[controlAddress] = struct{}{}
		if peer.DialAddress != "" {
			if err := validateHostPort(prefix+".dial_address", peer.DialAddress, false); err != nil {
				return err
			}
		}
		if peer.ControlDialAddress != "" {
			if err := validateHostPort(prefix+".control_dial_address", peer.ControlDialAddress, false); err != nil {
				return err
			}
		}
		if peer.NodeID == c.NodeID {
			selfAddress = peer.Address
		}
	}
	if len(c.RaftPeers) > 0 && selfAddress != c.RaftAdvertise {
		return fmt.Errorf(
			"raft_peers must map local node_id %q to raft_advertise %q exactly",
			c.NodeID, c.RaftAdvertise,
		)
	}
	return nil
}

// ControlBindAddress returns the node-control listener. An adjacent port keeps
// Raft and control traffic independently limitable by a host firewall.
func (c Config) ControlBindAddress() (string, error) {
	if c.ControlBind != "" {
		return c.ControlBind, nil
	}
	return adjacentPort(c.RaftBind)
}

func (c Config) ControlAdvertiseAddress() (string, error) {
	if c.ControlAdvertise != "" {
		return c.ControlAdvertise, nil
	}
	return adjacentPort(c.RaftAdvertise)
}

func (p RaftPeer) EffectiveControlAddress() (string, error) {
	if p.ControlAddress != "" {
		return p.ControlAddress, nil
	}
	return adjacentPort(p.Address)
}

func (p RaftPeer) EffectiveDialAddress() string {
	if p.DialAddress != "" {
		return p.DialAddress
	}
	return p.Address
}

func (p RaftPeer) EffectiveControlDialAddress() (string, error) {
	if p.ControlDialAddress != "" {
		return p.ControlDialAddress, nil
	}
	return p.EffectiveControlAddress()
}

func adjacentPort(address string) (string, error) {
	return portOffset(address, 1)
}

func (c Config) EnrollmentBindAddress() (string, error) {
	if c.EnrollmentBind != "" {
		return c.EnrollmentBind, nil
	}
	return portOffset(c.RaftBind, 2)
}

func (c Config) EnrollmentAdvertiseAddress() (string, error) {
	if c.EnrollmentAdvertise != "" {
		return c.EnrollmentAdvertise, nil
	}
	if !c.Bootstrap && len(c.RaftPeers) > 0 {
		return portOffset(c.RaftPeers[0].Address, 2)
	}
	return portOffset(c.RaftAdvertise, 2)
}

func (c Config) EffectiveEnrollmentIssuerID() string {
	if c.EnrollmentIssuerID != "" {
		return c.EnrollmentIssuerID
	}
	if c.Bootstrap || len(c.RaftPeers) == 0 {
		return c.NodeID
	}
	return c.RaftPeers[0].NodeID
}

func portOffset(address string, offset uint64) (string, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return "", err
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 || uint64(portNumber)+offset > 65535 {
		return "", errors.New("cannot derive an adjacent service port")
	}
	return net.JoinHostPort(host, strconv.FormatUint(portNumber+offset, 10)), nil
}

func validateHostPort(label, address string, allowWildcard bool) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%s must be host:port: %w", label, err)
	}
	if strings.TrimSpace(host) == "" {
		return fmt.Errorf("%s host is required", label)
	}
	if strings.TrimSpace(address) != address || strings.TrimSpace(host) != host {
		return fmt.Errorf("%s must not contain surrounding whitespace", label)
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil || portNumber == 0 {
		return fmt.Errorf("%s port must be an integer between 1 and 65535", label)
	}
	if !allowWildcard && (host == "*" || unspecifiedIP(host)) {
		return fmt.Errorf("%s must not use a wildcard host", label)
	}
	return nil
}

func unspecifiedIP(host string) bool {
	ip := net.ParseIP(host)
	return ip != nil && ip.IsUnspecified()
}

func validateProxyURL(label, value string, schemes ...string) error {
	if value == "" {
		return nil
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.Path != "" {
		return fmt.Errorf("%s must be an absolute proxy URL without path, query, or fragment", label)
	}
	for _, scheme := range schemes {
		if parsed.Scheme == scheme {
			return nil
		}
	}
	return fmt.Errorf("%s uses an unsupported proxy scheme", label)
}

func validateUpdateManifestURL(value string) error {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.Path == "" {
		return errors.New("update_manifest_url must be an absolute HTTPS URL without credentials, query, or fragment")
	}
	return nil
}
