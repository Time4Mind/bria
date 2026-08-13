package main

import (
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/Time4Mind/bria/internal/config"
	"github.com/Time4Mind/bria/internal/security"
)

type certificateRenewalState struct {
	Version    int                                `json:"version"`
	Request    security.CertificateRenewalRequest `json:"request"`
	PrivateKey []byte                             `json:"private_key_pem"`
}

type previousNodeCredentials struct {
	Certificate string    `json:"certificate"`
	PrivateKey  string    `json:"private_key"`
	RecordedAt  time.Time `json:"recorded_at"`
}

func createCertificateRenewalRequest(arguments []string) error {
	flags := flag.NewFlagSet("node cert-request", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "node config path")
	requestPath := flags.String("request", "", "public request output path")
	statePath := flags.String("state", "", "private renewal state output path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *requestPath == "" || *statePath == "" {
		return errors.New("config, request, and state are required")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	certificatePEM, err := os.ReadFile(nodeConfig.NodeCertificate)
	if err != nil {
		return fmt.Errorf("read current node certificate: %w", err)
	}
	privateKeyPEM, err := os.ReadFile(nodeConfig.NodePrivateKey)
	if err != nil {
		return fmt.Errorf("read current node private key: %w", err)
	}
	privateKey, err := security.ParseEd25519PrivateKey(privateKeyPEM)
	if err != nil {
		return err
	}
	request, newPrivateKey, err := security.NewCertificateRenewalRequest(
		nodeConfig.ClusterID, nodeConfig.NodeID, certificatePEM, privateKey,
		time.Now().UTC(), 30*time.Minute,
	)
	if err != nil {
		return err
	}
	newPrivateKeyPEM, err := security.MarshalEd25519PrivateKey(newPrivateKey)
	if err != nil {
		return err
	}
	state := certificateRenewalState{Version: 1, Request: request, PrivateKey: newPrivateKeyPEM}
	if err := writeJSONExclusive(*statePath, state); err != nil {
		return err
	}
	if err := writeJSONExclusive(*requestPath, request); err != nil {
		_ = os.Remove(*statePath)
		return err
	}
	fmt.Printf("created certificate renewal request %s for %s\n", request.RequestID, request.NodeID)
	return nil
}

func installCertificateRenewal(arguments []string) error {
	flags := flag.NewFlagSet("node cert-install", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "node config path")
	statePath := flags.String("state", "", "private renewal state path")
	responsePath := flags.String("response", "", "signed renewal response path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" || *statePath == "" || *responsePath == "" {
		return errors.New("config, state, and response are required")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	state, privateKey, err := loadCertificateRenewalState(*statePath)
	if err != nil {
		return err
	}
	var response security.CertificateRenewalResponse
	if err := readBoundedJSON(*responsePath, 128<<10, &response); err != nil {
		return fmt.Errorf("read renewal response: %w", err)
	}
	caPEM, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return fmt.Errorf("read cluster CA: %w", err)
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		return err
	}
	if state.Request.ClusterID != nodeConfig.ClusterID || state.Request.NodeID != nodeConfig.NodeID {
		return errors.New("renewal state does not belong to configured node")
	}
	if err := security.VerifyCertificateRenewalResponse(
		response, state.Request, privateKey, roots, time.Now().UTC(),
	); err != nil {
		return err
	}
	targetDir := filepath.Join(nodeConfig.DataDir, "pki", "renewals", response.RequestID)
	if nodeConfig.NodeCertificate == filepath.Join(targetDir, "node.crt") &&
		nodeConfig.NodePrivateKey == filepath.Join(targetDir, "node.key") {
		if err := verifyCredentialPair(
			nodeConfig, nodeConfig.NodeCertificate, nodeConfig.NodePrivateKey,
		); err != nil {
			return fmt.Errorf("verify already-installed credentials: %w", err)
		}
		if err := os.Remove(*statePath); err != nil {
			return fmt.Errorf("remove installed private renewal state: %w", err)
		}
		fmt.Printf("renewed certificate for %s was already installed\n", nodeConfig.NodeID)
		return nil
	}
	privateKeyPEM, err := security.MarshalEd25519PrivateKey(privateKey)
	if err != nil {
		return err
	}
	previous := previousNodeCredentials{
		Certificate: nodeConfig.NodeCertificate, PrivateKey: nodeConfig.NodePrivateKey,
		RecordedAt: state.Request.CreatedAt,
	}
	rotationDir, err := installCredentialFiles(nodeConfig, response, privateKeyPEM, previous)
	if err != nil {
		return err
	}
	nodeConfig.NodeCertificate = filepath.Join(rotationDir, "node.crt")
	nodeConfig.NodePrivateKey = filepath.Join(rotationDir, "node.key")
	if err := writeConfigAtomic(*configPath, nodeConfig); err != nil {
		return err
	}
	if err := os.Remove(*statePath); err != nil {
		return fmt.Errorf("certificate installed; remove private renewal state manually: %w", err)
	}
	fmt.Printf("installed renewed certificate for %s; restart this node to activate it\n", nodeConfig.NodeID)
	return nil
}

func rollbackCertificateRenewal(arguments []string) error {
	flags := flag.NewFlagSet("node cert-rollback", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	configPath := flags.String("config", "", "node config path")
	if err := flags.Parse(arguments); err != nil {
		return err
	}
	if flags.NArg() != 0 || *configPath == "" {
		return errors.New("config is required")
	}
	nodeConfig, err := config.Load(*configPath)
	if err != nil {
		return err
	}
	var previous previousNodeCredentials
	path := filepath.Join(filepath.Dir(nodeConfig.NodeCertificate), "previous.json")
	if err := readBoundedJSON(path, 16<<10, &previous); err != nil {
		return fmt.Errorf("read rollback metadata: %w", err)
	}
	if err := verifyCredentialPair(nodeConfig, previous.Certificate, previous.PrivateKey); err != nil {
		return fmt.Errorf("verify rollback credentials: %w", err)
	}
	nodeConfig.NodeCertificate = previous.Certificate
	nodeConfig.NodePrivateKey = previous.PrivateKey
	if err := writeConfigAtomic(*configPath, nodeConfig); err != nil {
		return err
	}
	fmt.Printf("restored previous certificate paths for %s; restart this node to activate them\n", nodeConfig.NodeID)
	return nil
}

func loadCertificateRenewalState(path string) (
	certificateRenewalState,
	ed25519.PrivateKey,
	error,
) {
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() ||
		(runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0) {
		return certificateRenewalState{}, nil, errors.New("private renewal state permissions are unsafe")
	}
	var state certificateRenewalState
	if err := readBoundedJSON(path, 128<<10, &state); err != nil || state.Version != 1 {
		return certificateRenewalState{}, nil, errors.New("invalid private renewal state")
	}
	privateKey, err := security.ParseEd25519PrivateKey(state.PrivateKey)
	if err != nil {
		return certificateRenewalState{}, nil, err
	}
	return state, privateKey, nil
}

func verifyCredentialPair(nodeConfig config.Config, certificatePath, privateKeyPath string) error {
	pair, err := tls.LoadX509KeyPair(certificatePath, privateKeyPath)
	if err != nil {
		return err
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return err
	}
	caPEM, err := os.ReadFile(nodeConfig.CACertificate)
	if err != nil {
		return err
	}
	roots, err := security.CertificatePool(caPEM)
	if err != nil {
		return err
	}
	return security.VerifyNodeCertificate(
		leaf, roots, nodeConfig.ClusterID, nodeConfig.NodeID,
		time.Now().UTC(), x509.ExtKeyUsageClientAuth,
	)
}

func writeJSONExclusive(path string, value any) error {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writeExclusive(path, append(encoded, '\n'))
}

func readBoundedJSON(path string, limit int64, target any) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > limit {
		return errors.New("JSON file is not regular or exceeds the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, limit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("JSON contains trailing values")
	}
	return nil
}
