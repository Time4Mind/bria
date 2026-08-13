package nodecontrol

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"

	"github.com/Time4Mind/bria/internal/security"
)

func serverTLSConfig(
	certificate tls.Certificate,
	roots *x509.CertPool,
	clusterID string,
) (*tls.Config, error) {
	if len(certificate.Certificate) == 0 || roots == nil || clusterID == "" {
		return nil, errors.New("node-control TLS identity is incomplete")
	}
	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS13,
		ClientAuth:   tls.RequireAnyClientCert,
		VerifyConnection: func(state tls.ConnectionState) error {
			return verifyClusterPeer(state, roots, clusterID, "", x509.ExtKeyUsageClientAuth)
		},
	}, nil
}

func clientTLSConfig(
	certificate tls.Certificate,
	roots *x509.CertPool,
	clusterID string,
	expectedNodeID string,
	expectedFingerprint string,
) (*tls.Config, error) {
	if len(certificate.Certificate) == 0 || roots == nil || clusterID == "" || expectedNodeID == "" {
		return nil, errors.New("node-control TLS peer identity is incomplete")
	}
	return &tls.Config{
		Certificates:       []tls.Certificate{certificate},
		MinVersion:         tls.VersionTLS13,
		InsecureSkipVerify: true, // replaced by CA + exact SPIFFE verification
		VerifyConnection: func(state tls.ConnectionState) error {
			if err := verifyClusterPeer(
				state, roots, clusterID, expectedNodeID, x509.ExtKeyUsageServerAuth,
			); err != nil {
				return err
			}
			if expectedFingerprint == "" {
				return nil
			}
			certificate := state.PeerCertificates[0]
			current, err := security.NodeCertificateFingerprint(certificate)
			if err != nil {
				return err
			}
			if current == expectedFingerprint {
				return nil
			}
			previous, present, err := security.PreviousNodeCertificateFingerprint(certificate)
			if err == nil && present && previous == expectedFingerprint {
				return nil
			}
			return errors.New("node-control peer uses a revoked key")
		},
	}, nil
}

func verifyClusterPeer(
	state tls.ConnectionState,
	roots *x509.CertPool,
	clusterID string,
	expectedNodeID string,
	usage x509.ExtKeyUsage,
) error {
	if len(state.PeerCertificates) == 0 {
		return errors.New("node-control peer sent no certificate")
	}
	certificate := state.PeerCertificates[0]
	nodeID := expectedNodeID
	if nodeID == "" {
		var err error
		nodeID, err = security.NodeIDFromCertificate(certificate, clusterID)
		if err != nil {
			return err
		}
	}
	return security.VerifyNodeCertificate(
		certificate, roots, clusterID, nodeID, time.Now(), usage,
	)
}
