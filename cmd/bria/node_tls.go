package main

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

func loadNodeTLS(nodeConfig config.Config) (tls.Certificate, *x509.CertPool, error) {
	certificate, err := tls.LoadX509KeyPair(nodeConfig.NodeCertificate, nodeConfig.NodePrivateKey)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("load node certificate: %w", err)
	}
	caPEM, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return tls.Certificate{}, nil, fmt.Errorf("read cluster CA: %w", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		return tls.Certificate{}, nil, err
	}
	return certificate, roots, nil
}

func tlsCertificateFingerprint(certificate tls.Certificate) (string, error) {
	if len(certificate.Certificate) == 0 {
		return "", errors.New("node certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse node certificate leaf: %w", err)
	}
	return security.NodeCertificateFingerprint(leaf)
}
