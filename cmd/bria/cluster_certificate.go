package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

func renewClusterNodeCertificate(arguments []string) error {
	flags := flag.NewFlagSet("cluster cert-renew", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "bootstrap node config path")
	requestPath := flags.String("request", "", "signed renewal request path")
	responsePath := flags.String("response", "", "signed response output path")
	confirmNodeID := flags.String("confirm-node-id", "", "exact node identifier confirmation")
	validFor := flags.Duration("valid-for", 365*24*time.Hour, "renewed certificate lifetime")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *requestPath == "" ||
		*responsePath == "" || *confirmNodeID == "" ||
		*validFor < 24*time.Hour || *validFor > 397*24*time.Hour {
		return errors.New("config, request, response, exact node confirmation, and validity from 24h to 397d are required")
	}
	source, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	if !source.Bootstrap || source.CAPrivateKey == "" {
		return errors.New("node certificates may only be renewed from the bootstrap CA config")
	}
	var request security.CertificateRenewalRequest
	if err := readBoundedJSON(*requestPath, 128<<10, &request); err != nil {
		return fmt.Errorf("read renewal request: %w", err)
	}
	if request.ClusterID != source.ClusterID {
		return errors.New("renewal request belongs to a different cluster")
	}
	if request.NodeID != *confirmNodeID {
		return fmt.Errorf("node confirmation does not match request identity %q", request.NodeID)
	}
	caCertificate, err := os.ReadFile(source.CACertificate)
	if err != nil {
		return fmt.Errorf("read CA certificate: %w", err)
	}
	caPrivateKey, err := os.ReadFile(source.CAPrivateKey)
	if err != nil {
		return fmt.Errorf("read CA private key: %w", err)
	}
	ca, err := security.ParseCA(caCertificate, caPrivateKey)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if now.Add(*validFor).After(ca.Certificate.NotAfter) {
		return errors.New("requested node certificate would outlive the cluster CA")
	}
	roots, err := security.CertificatePool(caCertificate)
	if err != nil {
		return err
	}
	response, err := security.IssueCertificateRenewal(ca, request, roots, now, *validFor)
	if err != nil {
		return err
	}
	if err := writeJSONExclusive(*responsePath, response); err != nil {
		return err
	}
	fmt.Printf("renewed certificate for %s; response written to %s\n", request.NodeID, *responsePath)
	return nil
}
