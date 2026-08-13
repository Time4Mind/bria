package nodecontrol

import (
	"crypto/x509"
	"errors"
	"net/http"
	"strings"

	"github.com/Time4Mind/bria/internal/security"
)

func (s *Server) authorizeMember(writer http.ResponseWriter, request *http.Request) (string, bool) {
	peerID, _, ok := s.authorizeMemberCertificate(writer, request)
	return peerID, ok
}

func (s *Server) authorizeMemberCertificate(
	writer http.ResponseWriter,
	request *http.Request,
) (string, *x509.Certificate, bool) {
	if request.Method != http.MethodPost ||
		!strings.HasPrefix(request.Header.Get("Content-Type"), "application/json") {
		http.Error(writer, "invalid request", http.StatusBadRequest)
		return "", nil, false
	}
	peerID, certificate, err := s.authenticatedPeer(request)
	if err != nil {
		http.Error(writer, "unauthorized", http.StatusUnauthorized)
		return "", nil, false
	}
	if !s.membership.IsMember(peerID) {
		http.Error(writer, "unknown member", http.StatusForbidden)
		return "", nil, false
	}
	if membership, ok := s.membership.(CertificateMembership); ok &&
		!membership.AuthorizeCertificate(peerID, certificate) {
		http.Error(writer, "revoked member identity", http.StatusForbidden)
		return "", nil, false
	}
	return peerID, certificate, true
}

func (s *Server) authenticatedPeer(request *http.Request) (string, *x509.Certificate, error) {
	if request.TLS == nil || len(request.TLS.PeerCertificates) == 0 {
		return "", nil, errors.New("missing client certificate")
	}
	certificate := request.TLS.PeerCertificates[0]
	nodeID, err := security.NodeIDFromCertificate(certificate, s.clusterID)
	return nodeID, certificate, err
}
